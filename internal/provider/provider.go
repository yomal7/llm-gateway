// Package provider defines the interface every LLM backend implements.
// The scheduler talks only to this interface, so adding OpenAI, Claude,
// or Ollama later never touches scheduling, queueing, or limiter code.
package provider

import (
	"context"
	"fmt"
	"time"
)

// Content is a single turn in a generation request. Role is typically
// "user" or "model".
type Content struct {
	Role string
	Text string
}

// GenerateRequest is the gateway's internal, provider-agnostic request
// shape. Both the OpenAI-compatible and Gemini-native API adapters
// translate into this before handing off to the scheduler.
type GenerateRequest struct {
	Model           string
	Contents        []Content
	MaxOutputTokens int
	Temperature     *float64
}

// Usage reports actual token consumption as reported by the provider,
// used to correct the gateway's own running TPM/RPD counters.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

type GenerateResponse struct {
	Text      string
	Usage     Usage
	ModelUsed string
}

// ErrorKind classifies a provider failure so the scheduler knows how to
// react: fail over to the next model, or stop and surface the error.
type ErrorKind int

const (
	ErrKindUnknown ErrorKind = iota
	ErrKindRateLimited
	ErrKindUnavailable
	ErrKindInvalidRequest
	ErrKindAuth
)

func (k ErrorKind) String() string {
	switch k {
	case ErrKindRateLimited:
		return "rate_limited"
	case ErrKindUnavailable:
		return "unavailable"
	case ErrKindInvalidRequest:
		return "invalid_request"
	case ErrKindAuth:
		return "auth"
	default:
		return "unknown"
	}
}

// Error wraps a provider-side failure with enough context for the
// scheduler to react correctly, including an optional retry delay
// parsed from the provider's own response where available.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	RetryAfter time.Duration // zero if the provider didn't specify one
}

func (e *Error) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("provider error (status %d, %s): %s (retry after %s)", e.StatusCode, e.Kind, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("provider error (status %d, %s): %s", e.StatusCode, e.Kind, e.Message)
}

// Retryable reports whether the scheduler should try the next model in
// the priority list rather than surfacing the error to the caller.
func (e *Error) Retryable() bool {
	return e.Kind == ErrKindRateLimited || e.Kind == ErrKindUnavailable
}

// Provider is implemented by each backend behind the same interface so
// the scheduler never needs to know which one it's talking to.
type Provider interface {
	Name() string
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}
