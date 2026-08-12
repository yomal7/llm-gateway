package geminiapi

import (
	"context"
	"encoding/json"
	"io"
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
	response  func(req provider.GenerateRequest) (provider.GenerateResponse, error)
	lastBody  []byte
	sawTools  bool
	sawSystem bool
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	f.lastBody = req.Body
	var parsed map[string]json.RawMessage
	if json.Unmarshal(req.Body, &parsed) == nil {
		_, f.sawTools = parsed["tools"]
		_, f.sawSystem = parsed["systemInstruction"]
	}
	if f.response != nil {
		return f.response(req)
	}
	return provider.GenerateResponse{
		Body:      []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello from ` + req.Model + `"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`),
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

	respBody, _ := io.ReadAll(resp.Body)
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if _, ok := parsed["candidates"]; !ok {
		t.Errorf("response missing candidates: %s", respBody)
	}
	if _, ok := parsed["usageMetadata"]; !ok {
		t.Errorf("response missing usageMetadata: %s", respBody)
	}
}

func TestGenerateContent_ForwardsToolsAndSystemInstructionVerbatim(t *testing.T) {
	fp := &fakeProvider{}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 100000, RPD: 100}})

	body := `{
		"systemInstruction": {"parts": [{"text": "you are a vulnerability triage analyst"}]},
		"contents": [{"role": "user", "parts": [{"text": "investigate this alert"}]}],
		"tools": [{"functionDeclarations": [{"name": "check_kev_status"}, {"name": "check_fix_version"}]}]
	}`
	resp, err := http.Post(server.URL+"/v1beta/models/m:generateContent", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !fp.sawTools {
		t.Error("provider never received a 'tools' field — the ReAct loop's function calling would silently break")
	}
	if !fp.sawSystem {
		t.Error("provider never received a 'systemInstruction' field — the agent's system prompt would silently be dropped")
	}
}

func TestGenerateContent_FunctionCallResponseSurvivesUntouched(t *testing.T) {
	fp := &fakeProvider{
		response: func(req provider.GenerateRequest) (provider.GenerateResponse, error) {
			return provider.GenerateResponse{
				Body:      []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"check_kev_status","args":{"group_id":"org.apache.logging.log4j"}}}]}}]}`),
				ModelUsed: req.Model,
			}, nil
		},
	}
	server := newTestServer(t, fp, []limiter.Model{{Name: "m", RPM: 10, TPM: 100000, RPD: 100}})

	resp, err := http.Post(server.URL+"/v1beta/models/m:generateContent", "application/json",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "functionCall") {
		t.Errorf("response body lost the functionCall part: %s", respBody)
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