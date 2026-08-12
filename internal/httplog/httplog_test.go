package httplog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withCapturedLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fn()
	return buf.String()
}

func TestMiddleware_PassesThroughResponse(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	})

	server := httptest.NewServer(Middleware(inner))
	defer server.Close()

	resp, err := http.Get(server.URL + "/brew")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d — middleware must not alter the response", resp.StatusCode, http.StatusTeapot)
	}
}

func TestMiddleware_LogsStatusPathAndDuration(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	server := httptest.NewServer(Middleware(inner))
	defer server.Close()

	logs := withCapturedLogs(t, func() {
		resp, err := http.Get(server.URL + "/v1beta/models/x:generateContent")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
	})

	line := strings.TrimSpace(logs)
	if line == "" {
		t.Fatal("expected a log line, got none")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("log line isn't valid JSON: %v\nline: %s", err, line)
	}

	if parsed["msg"] != "http_access" {
		t.Errorf("msg = %v, want http_access", parsed["msg"])
	}
	if parsed["path"] != "/v1beta/models/x:generateContent" {
		t.Errorf("path = %v, want /v1beta/models/x:generateContent", parsed["path"])
	}
	if parsed["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v, want %d", parsed["status"], http.StatusCreated)
	}
	if _, ok := parsed["duration_ms"]; !ok {
		t.Error("missing duration_ms field")
	}
}

func TestMiddleware_DefaultsTo200WhenWriteHeaderNeverCalled(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("no explicit status set"))
	})

	server := httptest.NewServer(Middleware(inner))
	defer server.Close()

	logs := withCapturedLogs(t, func() {
		resp, err := http.Get(server.URL + "/healthz")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
	})

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs)), &parsed); err != nil {
		t.Fatalf("log line isn't valid JSON: %v", err)
	}
	if parsed["status"] != float64(http.StatusOK) {
		t.Errorf("status = %v, want %d (implicit 200 like net/http's own default)", parsed["status"], http.StatusOK)
	}
}