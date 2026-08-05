package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleYAML = `
server:
  port: 9090
  default_strategy: failover
  default_queue_timeout_seconds: 15

gemini:
  base_url: https://generativelanguage.googleapis.com
  api_key_env: TEST_GEMINI_KEY

limits:
  max_input_tokens: 20000
  max_output_tokens: 4096

models:
  - name: gemini-3.1-flash-lite
    rpm: 15
    tpm: 250000
    rpd: 500
  - name: gemini-2.5-flash
    rpm: 5
    tpm: 250000
    rpd: 20
`

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

func TestLoad_Valid(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY", "fake-key")
	path := writeTempConfig(t, sampleYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.DefaultStrategy != StrategyFailover {
		t.Errorf("strategy = %q, want failover", cfg.Server.DefaultStrategy)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(cfg.Models))
	}
	// List order is failover priority order — this must be preserved.
	if cfg.Models[0].Name != "gemini-3.1-flash-lite" {
		t.Errorf("first model = %q, want gemini-3.1-flash-lite (priority order matters)", cfg.Models[0].Name)
	}
	if cfg.APIKey() != "fake-key" {
		t.Errorf("APIKey() = %q, want fake-key", cfg.APIKey())
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY_DEFAULTS", "fake-key")
	path := writeTempConfig(t, `
gemini:
  api_key_env: TEST_GEMINI_KEY_DEFAULTS
models:
  - name: gemini-3.1-flash-lite
    rpm: 15
    tpm: 250000
    rpd: 500
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("default port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.DefaultStrategy != StrategyQueue {
		t.Errorf("default strategy = %q, want queue", cfg.Server.DefaultStrategy)
	}
}

func TestLoad_MissingAPIKeyEnv(t *testing.T) {
	path := writeTempConfig(t, `
gemini:
  api_key_env: TEST_GEMINI_KEY_DEFINITELY_UNSET
models:
  - name: gemini-3.1-flash-lite
    rpm: 15
    tpm: 250000
    rpd: 500
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when the referenced env var is unset")
	}
}

func TestLoad_NoModels(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY2", "fake-key")
	path := writeTempConfig(t, `
gemini:
  api_key_env: TEST_GEMINI_KEY2
models: []
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when no models are configured")
	}
}

func TestLoad_DuplicateModelNames(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY3", "fake-key")
	path := writeTempConfig(t, `
gemini:
  api_key_env: TEST_GEMINI_KEY3
models:
  - name: gemini-3.1-flash-lite
    rpm: 15
    tpm: 250000
    rpd: 500
  - name: gemini-3.1-flash-lite
    rpm: 10
    tpm: 100000
    rpd: 100
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for duplicate model names")
	}
}

func TestLoad_InvalidStrategy(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY4", "fake-key")
	path := writeTempConfig(t, `
server:
  default_strategy: sometimes
gemini:
  api_key_env: TEST_GEMINI_KEY4
models:
  - name: gemini-3.1-flash-lite
    rpm: 15
    tpm: 250000
    rpd: 500
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an invalid strategy value")
	}
}
