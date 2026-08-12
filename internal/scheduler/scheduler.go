package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yomal7/llm-gateway/internal/limiter"
	"github.com/yomal7/llm-gateway/internal/provider"
)

type Strategy string

const (
	StrategyQueue    Strategy = "queue"
	StrategyFailover Strategy = "failover"
)

const minWaitFloor = 5 * time.Millisecond

type Request struct {
	Contents        []provider.Content
	MaxOutputTokens int
	Temperature     *float64

	EstimatedTokens int

	Strategy Strategy

	Timeout time.Duration
}

type Attempt struct {
	Model   string
	Outcome string
	Reason  string
	Wait    time.Duration
	Err     error
}

type Result struct {
	Response  provider.GenerateResponse
	ModelUsed string
	Attempts  []Attempt
}

type modelEntry struct {
	name    string
	limiter *limiter.Limiter
}

type Scheduler struct {
	provider        provider.Provider
	models          []modelEntry
	defaultStrategy Strategy
	defaultTimeout  time.Duration
}

func New(p provider.Provider, models []limiter.Model, defaultStrategy Strategy, defaultTimeout time.Duration) *Scheduler {
	entries := make([]modelEntry, len(models))
	for i, m := range models {
		entries[i] = modelEntry{name: m.Name, limiter: limiter.New(m)}
	}
	if defaultStrategy != StrategyQueue && defaultStrategy != StrategyFailover {
		defaultStrategy = StrategyQueue
	}
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	return &Scheduler{
		provider:        p,
		models:          entries,
		defaultStrategy: defaultStrategy,
		defaultTimeout:  defaultTimeout,
	}
}

// EstimateInputTokens gives a rough pre-call token estimate for TPM
// admission, using the common ~4-characters-per-token heuristic.
// Gemini's TPM limit counts input tokens only, so output tokens are
// deliberately not included here. This is a coarse estimate by
// design — ReportActualUsage corrects the limiter's TPM bucket against
// the real count as soon as a response comes back.
func EstimateInputTokens(contents []provider.Content) int {
	var chars int
	for _, c := range contents {
		chars += len(c.Text)
	}
	return chars/4 + 1
}

// Generate walks the priority list, admitting locally against each
// model's limiter and calling the provider once admitted. It returns
// as soon as a model succeeds, or an error once every model has been
// exhausted or the context is done.
func (s *Scheduler) Generate(ctx context.Context, req Request) (Result, error) {
	strategy := req.Strategy
	if strategy != StrategyQueue && strategy != StrategyFailover {
		strategy = s.defaultStrategy
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = s.defaultTimeout
	}
	estimatedTokens := req.EstimatedTokens
	if estimatedTokens <= 0 {
		estimatedTokens = EstimateInputTokens(req.Contents)
	}

	var attempts []Attempt

	for _, entry := range s.models {
		admitted, reason, wait, err := s.admit(ctx, entry, estimatedTokens, strategy, timeout)
		if err != nil {
			// Context canceled or deadline exceeded — the caller gave
			// up, so there's no point trying further models.
			attempts = append(attempts, Attempt{Model: entry.name, Outcome: "rejected_locally", Err: err})
			return Result{Attempts: attempts}, err
		}
		if !admitted {
			attempts = append(attempts, Attempt{Model: entry.name, Outcome: "rejected_locally", Reason: reason, Wait: wait})
			continue
		}

		resp, callErr := s.provider.Generate(ctx, provider.GenerateRequest{
			Model:           entry.name,
			Contents:        req.Contents,
			MaxOutputTokens: req.MaxOutputTokens,
			Temperature:     req.Temperature,
		})
		if callErr == nil {
			// TPM only tracks input tokens (per Gemini's documented
			// limits), so only correct against the real input count.
			entry.limiter.ReportActualUsage(estimatedTokens, resp.Usage.InputTokens)
			attempts = append(attempts, Attempt{Model: entry.name, Outcome: "success"})
			return Result{Response: resp, ModelUsed: entry.name, Attempts: attempts}, nil
		}

		// A real call was made and failed. Never release the local
		// reservation here — see limiter.Release's doc comment for why.
		var provErr *provider.Error
		if errors.As(callErr, &provErr) && provErr.Retryable() {
			attempts = append(attempts, Attempt{Model: entry.name, Outcome: "provider_error", Reason: provErr.Kind.String(), Err: callErr})
			continue // always fail over on a retryable provider error
		}

		// Non-retryable (bad request, auth, or a non-provider error
		// like a network failure) — stop immediately, failing over
		// won't fix it.
		attempts = append(attempts, Attempt{Model: entry.name, Outcome: "provider_error", Err: callErr})
		return Result{Attempts: attempts}, callErr
	}

	return Result{Attempts: attempts}, fmt.Errorf("no model admitted the request: all %d configured models are rate-limited or exhausted", len(s.models))
}

func (s *Scheduler) admit(ctx context.Context, entry modelEntry, estimatedTokens int, strategy Strategy, timeout time.Duration) (admitted bool, reason string, waited time.Duration, err error) {
	deadline := time.Now().Add(timeout)

	for {
		d := entry.limiter.TryAdmit(estimatedTokens)
		if d.Admitted {
			return true, "", 0, nil
		}
		if d.Reason == "rpd" {
			return false, d.Reason, d.Wait, nil
		}
		if strategy == StrategyFailover {
			return false, d.Reason, d.Wait, nil
		}

		wait := d.Wait
		if wait < minWaitFloor {
			wait = minWaitFloor
		}
		if wait > time.Until(deadline) {
			return false, d.Reason, d.Wait, nil
		}

		select {
		case <-ctx.Done():
			return false, d.Reason, 0, ctx.Err()
		case <-time.After(wait):
		}
	}
}