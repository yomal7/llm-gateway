package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yomal7/llm-gateway/internal/provider"
)

func TestGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/gemini-3.1-flash-lite:generateContent"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		resp := generateContentResponse{
			Candidates: []candidate{{
				Content: geminiContent{Parts: []geminiPart{{Text: "hello there"}}},
			}},
			UsageMetadata: usageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 3, TotalTokenCount: 13},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	got, err := client.Generate(context.Background(), provider.GenerateRequest{
		Model:    "gemini-3.1-flash-lite",
		Contents: []provider.Content{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Text != "hello there" {
		t.Errorf("text = %q, want %q", got.Text, "hello there")
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want input=10 output=3", got.Usage)
	}
	if got.ModelUsed != "gemini-3.1-flash-lite" {
		t.Errorf("modelUsed = %q, want gemini-3.1-flash-lite", got.ModelUsed)
	}
}

func TestGenerate_RateLimitedWithRetryInfo(t *testing.T) {
	// This mirrors the exact 429 shape seen in the triage-tool logs:
	// RESOURCE_EXHAUSTED with a RetryInfo detail carrying retryDelay.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{
			"error": {
				"code": 429,
				"message": "Quota exceeded for metric: generate_content_free_tier_requests",
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{"@type": "type.googleapis.com/google.rpc.QuotaFailure"},
					{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "25.365137511s"}
				]
			}
		}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-3.1-flash-lite"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	provErr, ok := err.(*provider.Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T", err)
	}
	if provErr.Kind != provider.ErrKindRateLimited {
		t.Errorf("kind = %v, want ErrKindRateLimited", provErr.Kind)
	}
	if !provErr.Retryable() {
		t.Error("expected a rate-limited error to be retryable (scheduler should fail over)")
	}
	wantDelay := 25*time.Second + 365137511
	if provErr.RetryAfter != time.Duration(wantDelay) {
		t.Errorf("retryAfter = %v, want %v", provErr.RetryAfter, time.Duration(wantDelay))
	}
}

func TestGenerate_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": {"code": 503, "message": "backend overloaded", "status": "UNAVAILABLE"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-2.5-flash"})
	provErr, ok := err.(*provider.Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T", err)
	}
	if provErr.Kind != provider.ErrKindUnavailable || !provErr.Retryable() {
		t.Errorf("unexpected classification: %+v", provErr)
	}
}

func TestGenerate_InvalidRequestIsNotRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "invalid argument"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-2.5-flash"})
	provErr, ok := err.(*provider.Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T", err)
	}
	if provErr.Kind != provider.ErrKindInvalidRequest {
		t.Errorf("kind = %v, want ErrKindInvalidRequest", provErr.Kind)
	}
	if provErr.Retryable() {
		t.Error("a bad request should not be retryable — failing over won't fix a malformed request")
	}
}
