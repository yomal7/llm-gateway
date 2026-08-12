package provider

import (
	"context"
	"fmt"
	"time"
)

type GenerateRequest struct {
	Model string
	Body  []byte
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type GenerateResponse struct {
	Body      []byte
	Usage     Usage
	ModelUsed string
}

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

type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("provider error (status %d, %s): %s (retry after %s)", e.StatusCode, e.Kind, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("provider error (status %d, %s): %s", e.StatusCode, e.Kind, e.Message)
}

func (e *Error) Retryable() bool {
	return e.Kind == ErrKindRateLimited || e.Kind == ErrKindUnavailable
}

type Provider interface {
	Name() string
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}