package geminiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yomal7/llm-gateway/internal/limiter"
	"github.com/yomal7/llm-gateway/internal/provider"
	"github.com/yomal7/llm-gateway/internal/scheduler"
)

type fakeProvider struct {
	response func(req provider.GenerateRequest) (provider.GenerateResponse, error)
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	if f.response != nil {
		return f.response(req)
	}
	return provider.GenerateResponse{
		Text:      "hello from " + req.Model,
		ModelUsed: req.Model,
		Usage:     provider.Usage{InputTokens: 3, OutputTokens: 4},
	}, nil
}

func newTestServer(t *testing.T, fp *fakeProvider, models []limiter.Model) *httptest.Server {
	t.Helper()
	sched := scheduler.New(fp, models, scheduler.StrategyQueue, time.Second)
	mux := http.NewServeMux()
	RegisterRoutes(mux, sched)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestGenerateContent_Success(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "gemini-3.1-flash-lite", RPM: 100, TPM: 100000, RPD: 1000}})

	body := `{"contents":[{"role":"user","parts":[{"text":"hi there"}]}],"generationConfig":{"maxOutputTokens":100}}`
	resp, err := http.Post(server.URL+"/v1beta/models/gemini-3.1-flash-lite:generateContent", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Gateway-Model-Used"); got != "gemini-3.1-flash-lite" {
		t.Errorf("X-Gateway-Model-Used = %q, want %q", got, "gemini-3.1-flash-lite")
	}

	var parsed generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(parsed.Candidates) != 1 || parsed.Candidates[0].Content.Parts[0].Text != "hello from gemini-3.1-flash-lite" {
		t.Errorf("unexpected candidates: %+v", parsed.Candidates)
	}
	if parsed.UsageMetadata.PromptTokenCount != 3 || parsed.UsageMetadata.CandidatesTokenCount != 4 {
		t.Errorf("unexpected usage: %+v", parsed.UsageMetadata)
	}
}

func TestGenerateContent_WrongActionSuffixIs404(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 100}})

	resp, err := http.Post(server.URL+"/v1beta/models/m:countTokens", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGenerateContent_InvalidJSONIs400(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 100}})

	resp, err := http.Post(server.URL+"/v1beta/models/m:generateContent", "application/json", strings.NewReader(`{not valid json`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestGenerateContent_InvalidStrategyHeaderIs400(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 100}})

	httpReq, _ := http.NewRequest(http.MethodPost, server.URL+"/v1beta/models/m:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	httpReq.Header.Set("X-Gateway-Strategy", "yolo")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestGenerateContent_ValidStrategyHeaderAccepted(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 100}})

	httpReq, _ := http.NewRequest(http.MethodPost, server.URL+"/v1beta/models/m:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	httpReq.Header.Set("X-Gateway-Strategy", "failover")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestGenerateContent_ProviderErrorPropagatesStatusCode(t *testing.T) {
	fp := &fakeProvider{
		response: func(req provider.GenerateRequest) (provider.GenerateResponse, error) {
			return provider.GenerateResponse{}, &provider.Error{Kind: provider.ErrKindInvalidRequest, StatusCode: 400, Message: "bad request from provider"}
		},
	}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 100}})

	resp, err := http.Post(server.URL+"/v1beta/models/m:generateContent", "application/json",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (propagated from the provider error)", resp.StatusCode)
	}
}

func TestGenerateContent_AllModelsExhaustedIs503(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 10000, RPD: 0}})

	resp, err := http.Post(server.URL+"/v1beta/models/m:generateContent", "application/json",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}