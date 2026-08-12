package scheduler

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/yomal7/llm-gateway/internal/limiter"
	"github.com/yomal7/llm-gateway/internal/provider"
)

type fakeProvider struct {
	mu    sync.Mutex
	calls []provider.GenerateRequest

	responses map[string]func(req provider.GenerateRequest) (provider.GenerateResponse, error)
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{responses: map[string]func(req provider.GenerateRequest) (provider.GenerateResponse, error){}}
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()

	if fn, ok := f.responses[req.Model]; ok {
		return fn(req)
	}
	return provider.GenerateResponse{
		Body:      []byte(`{"candidates":[{"content":{"parts":[{"text":"ok from ` + req.Model + `"}]}}]}`),
		ModelUsed: req.Model,
		Usage:     provider.Usage{InputTokens: 5, OutputTokens: 5},
	}, nil
}

func (f *fakeProvider) modelsCalled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Model
	}
	return out
}

func generousModel(name string) limiter.Model {
	return limiter.Model{Name: name, RPM: 1000, TPM: 1_000_000, RPD: 10000}
}

func req() Request {
	return Request{Body: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)}
}

func TestGenerate_SucceedsOnFirstModel(t *testing.T) {
	fp := newFakeProvider()
	sched := New(fp, []limiter.Model{generousModel("a"), generousModel("b")}, StrategyQueue, time.Second)

	result, err := sched.Generate(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "a" {
		t.Errorf("ModelUsed = %q, want %q", result.ModelUsed, "a")
	}
	if calls := fp.modelsCalled(); len(calls) != 1 || calls[0] != "a" {
		t.Errorf("calls = %v, want [a]", calls)
	}
}

func TestGenerate_ForwardsRequestBodyVerbatim(t *testing.T) {
	fp := newFakeProvider()
	sched := New(fp, []limiter.Model{generousModel("a")}, StrategyQueue, time.Second)

	body := []byte(`{"systemInstruction":{"parts":[{"text":"be helpful"}]},"tools":[{"functionDeclarations":[{"name":"check_kev_status"}]}],"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if _, err := sched.Generate(context.Background(), Request{Body: body}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := fp.calls
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	var got, want any
	if err := json.Unmarshal(calls[0].Body, &got); err != nil {
		t.Fatalf("unmarshaling forwarded body: %v", err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatalf("unmarshaling original body: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("forwarded body = %s, want %s (tools/systemInstruction must survive the scheduler untouched)", gotJSON, wantJSON)
	}
}

func TestGenerate_FailsOverOnRetryableProviderError(t *testing.T) {
	fp := newFakeProvider()
	fp.responses["a"] = func(req provider.GenerateRequest) (provider.GenerateResponse, error) {
		return provider.GenerateResponse{}, &provider.Error{Kind: provider.ErrKindRateLimited, StatusCode: 429, Message: "quota exceeded"}
	}
	sched := New(fp, []limiter.Model{generousModel("a"), generousModel("b")}, StrategyQueue, time.Second)

	result, err := sched.Generate(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "b" {
		t.Errorf("ModelUsed = %q, want %q (should have failed over)", result.ModelUsed, "b")
	}
	if calls := fp.modelsCalled(); len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("calls = %v, want [a b]", calls)
	}
}

func TestGenerate_FailsOverOnModelNotFound(t *testing.T) {
	// Regression test for a real failure: a misconfigured or
	// decommissioned model name in the priority list must not sink
	// the whole request when a working model is next in line.
	fp := newFakeProvider()
	fp.responses["a"] = func(req provider.GenerateRequest) (provider.GenerateResponse, error) {
		return provider.GenerateResponse{}, &provider.Error{Kind: provider.ErrKindNotFound, StatusCode: 404, Message: "models/a is not found"}
	}
	sched := New(fp, []limiter.Model{generousModel("a"), generousModel("b")}, StrategyQueue, time.Second)

	result, err := sched.Generate(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "b" {
		t.Errorf("ModelUsed = %q, want %q (a 404 on model a should fail over to model b)", result.ModelUsed, "b")
	}
	if calls := fp.modelsCalled(); len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("calls = %v, want [a b]", calls)
	}
}

func TestGenerate_NonRetryableProviderErrorStopsImmediately(t *testing.T) {
	fp := newFakeProvider()
	fp.responses["a"] = func(req provider.GenerateRequest) (provider.GenerateResponse, error) {
		return provider.GenerateResponse{}, &provider.Error{Kind: provider.ErrKindInvalidRequest, StatusCode: 400, Message: "bad request"}
	}
	sched := New(fp, []limiter.Model{generousModel("a"), generousModel("b")}, StrategyQueue, time.Second)

	_, err := sched.Generate(context.Background(), req())
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls := fp.modelsCalled(); len(calls) != 1 || calls[0] != "a" {
		t.Errorf("calls = %v, want [a] only — a non-retryable error should not fail over", calls)
	}
}

func TestGenerate_FailoverModeSkipsLocallyExhaustedModelWithoutWaiting(t *testing.T) {
	fp := newFakeProvider()
	modelA := limiter.Model{Name: "a", RPM: 1, TPM: 1_000_000, RPD: 100}
	sched := New(fp, []limiter.Model{modelA, generousModel("b")}, StrategyQueue, time.Second)

	sched.models[0].limiter.TryAdmit(1)

	start := time.Now()
	result, err := sched.Generate(context.Background(), Request{Body: req().Body, Strategy: StrategyFailover})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "b" {
		t.Errorf("ModelUsed = %q, want %q", result.ModelUsed, "b")
	}
	if calls := fp.modelsCalled(); len(calls) != 1 || calls[0] != "b" {
		t.Errorf("calls = %v, want [b] only — failover mode must not call the provider for an unadmitted model", calls)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("failover mode took %v, expected a near-instant non-blocking check", elapsed)
	}
}

func TestGenerate_QueueModeWaitsForRPMCapacityThenSucceeds(t *testing.T) {
	fp := newFakeProvider()
	modelA := limiter.Model{Name: "a", RPM: 600, TPM: 10_000_000, RPD: 100000}
	sched := New(fp, []limiter.Model{modelA}, StrategyQueue, 2*time.Second)

	for i := 0; i < 600; i++ {
		sched.models[0].limiter.TryAdmit(1)
	}

	start := time.Now()
	result, err := sched.Generate(context.Background(), req())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "a" {
		t.Errorf("ModelUsed = %q, want %q", result.ModelUsed, "a")
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("expected to actually wait for refill (~100ms), only took %v", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("wait took %v, expected well under the 2s timeout", elapsed)
	}
}

func TestGenerate_QueueModeGivesUpBeforeExceedingTimeoutAndFailsOver(t *testing.T) {
	fp := newFakeProvider()
	modelA := limiter.Model{Name: "a", RPM: 1, TPM: 1_000_000, RPD: 100}
	sched := New(fp, []limiter.Model{modelA, generousModel("b")}, StrategyQueue, 50*time.Millisecond)

	sched.models[0].limiter.TryAdmit(1)

	start := time.Now()
	result, err := sched.Generate(context.Background(), req())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "b" {
		t.Errorf("ModelUsed = %q, want %q", result.ModelUsed, "b")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("took %v — should have given up on model a's 60s wait almost immediately given a 50ms timeout", elapsed)
	}
}

func TestGenerate_ContextCanceledStopsImmediately(t *testing.T) {
	fp := newFakeProvider()
	sched := New(fp, []limiter.Model{generousModel("a")}, StrategyQueue, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	modelLimiter := sched.models[0].limiter
	for i := 0; i < 1000; i++ {
		modelLimiter.TryAdmit(1)
	}

	_, err := sched.Generate(ctx, req())
	if err == nil {
		t.Fatal("expected an error from a canceled context")
	}
	if calls := fp.modelsCalled(); len(calls) != 0 {
		t.Errorf("calls = %v, want none — canceled context should stop before calling the provider", calls)
	}
}

func TestGenerate_AllModelsExhaustedReturnsError(t *testing.T) {
	fp := newFakeProvider()
	exhausted := limiter.Model{Name: "a", RPM: 10, TPM: 10000, RPD: 0}
	sched := New(fp, []limiter.Model{exhausted}, StrategyFailover, time.Second)

	_, err := sched.Generate(context.Background(), req())
	if err == nil {
		t.Fatal("expected an error when every model is exhausted")
	}
	if calls := fp.modelsCalled(); len(calls) != 0 {
		t.Errorf("calls = %v, want none — the provider should never be called for an unadmitted model", calls)
	}
}

func TestGenerate_AutoEstimatesTokensWhenNotProvided(t *testing.T) {
	fp := newFakeProvider()
	tinyTPM := limiter.Model{Name: "a", RPM: 1000, TPM: 1, RPD: 1000}
	sched := New(fp, []limiter.Model{tinyTPM, generousModel("b")}, StrategyFailover, time.Second)

	result, err := sched.Generate(context.Background(), Request{
		Body: []byte(`{"contents":[{"role":"user","parts":[{"text":"this is a longer message that estimates to well over one token"}]}]}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelUsed != "b" {
		t.Errorf("ModelUsed = %q, want %q — auto-estimated tokens should have exceeded model a's tiny TPM", result.ModelUsed, "b")
	}
}

func TestEstimateInputTokens_RoughlyFourCharsPerToken(t *testing.T) {
	body := []byte("12345678") // 8 bytes
	got := EstimateInputTokens(body)
	want := 8/4 + 1
	if got != want {
		t.Errorf("EstimateInputTokens() = %d, want %d", got, want)
	}
}