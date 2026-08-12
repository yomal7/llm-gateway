package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yomal7/llm-gateway/internal/provider"
)

func TestGenerate_Success(t *testing.T) {
	const reqBody = `{
		"systemInstruction": {"parts": [{"text": "you are a helpful analyst"}]},
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"tools": [{"functionDeclarations": [{"name": "check_kev_status"}]}]
	}`
	const respBody = `{
		"candidates": [{"content": {"role": "model", "parts": [{"functionCall": {"name": "check_kev_status", "args": {}}}]}}],
		"usageMetadata": {"promptTokenCount": 42, "candidatesTokenCount": 7, "totalTokenCount": 49}
	}`

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/gemini-3.1-flash-lite:generateContent"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	got, err := client.Generate(context.Background(), provider.GenerateRequest{
		Model: "gemini-3.1-flash-lite",
		Body:  []byte(reqBody),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !jsonEqual(t, gotBody, []byte(reqBody)) {
		t.Errorf("request body forwarded = %s, want %s (tools/systemInstruction must survive untouched)", gotBody, reqBody)
	}
	if !jsonEqual(t, got.Body, []byte(respBody)) {
		t.Errorf("response body = %s, want %s (functionCall parts must survive untouched)", got.Body, respBody)
	}
	if got.Usage.InputTokens != 42 || got.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want input=42 output=7", got.Usage)
	}
	if got.ModelUsed != "gemini-3.1-flash-lite" {
		t.Errorf("modelUsed = %q, want gemini-3.1-flash-lite", got.ModelUsed)
	}
}

func TestGenerate_RateLimitedWithRetryInfo(t *testing.T) {
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
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-3.1-flash-lite", Body: []byte(`{}`)})
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
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-2.5-flash", Body: []byte(`{}`)})
	provErr, ok := err.(*provider.Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T", err)
	}
	if provErr.Kind != provider.ErrKindUnavailable || !provErr.Retryable() {
		t.Errorf("unexpected classification: %+v", provErr)
	}
}

func TestGenerate_ModelNotFoundIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": 404, "message": "models/gemma-4-26b is not found for API version v1beta, or is not supported for generateContent."}}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemma-4-26b", Body: []byte(`{}`)})
	provErr, ok := err.(*provider.Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T", err)
	}
	if provErr.Kind != provider.ErrKindNotFound {
		t.Errorf("kind = %v, want ErrKindNotFound", provErr.Kind)
	}
	if !provErr.Retryable() {
		t.Error("expected a model-not-found error to be retryable (scheduler should try the next model)")
	}
}

func TestGenerate_InvalidRequestIsNotRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "invalid argument"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	_, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "gemini-2.5-flash", Body: []byte(`{}`)})
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

func TestGenerate_UsageExtractionFailureYieldsZeroUsageNotError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "no usage field here"}]}}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	got, err := client.Generate(context.Background(), provider.GenerateRequest{Model: "m", Body: []byte(`{}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Usage.InputTokens != 0 || got.Usage.OutputTokens != 0 {
		t.Errorf("usage = %+v, want zero value when usageMetadata is absent", got.Usage)
	}
	if len(got.Body) == 0 {
		t.Error("response body should still be forwarded even when usage extraction finds nothing")
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshaling a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshaling b: %v", err)
	}
	aNorm, _ := json.Marshal(av)
	bNorm, _ := json.Marshal(bv)
	return string(aNorm) == string(bNorm)
}