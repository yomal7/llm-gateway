// Package config loads and validates the gateway's YAML configuration.
// The Gemini API key is deliberately kept out of the config file itself
// (referenced by env var name instead) so the file is safe to commit.
package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

type Strategy string

const (
	StrategyQueue    Strategy = "queue"
	StrategyFailover Strategy = "failover"
)

type ServerConfig struct {
	Port                       int      `yaml:"port"`
	DefaultStrategy            Strategy `yaml:"default_strategy"`
	DefaultQueueTimeoutSeconds int      `yaml:"default_queue_timeout_seconds"`
}

type GeminiConfig struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

// LimitsConfig holds admin-set hard caps, independent of whatever
// Gemini itself allows — e.g. capping output tokens to control spend
// even when the provider would allow more.
type LimitsConfig struct {
	MaxInputTokens  int `yaml:"max_input_tokens"`
	MaxOutputTokens int `yaml:"max_output_tokens"`
}

type StorageConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

// ModelConfig describes one model's free-tier limits. List order in
// the YAML file IS the failover priority order — the gateway tries
// models top to bottom.
type ModelConfig struct {
	Name string `yaml:"name"`
	RPM  int    `yaml:"rpm"`
	TPM  int    `yaml:"tpm"`
	RPD  int    `yaml:"rpd"`
}

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Gemini  GeminiConfig  `yaml:"gemini"`
	Limits  LimitsConfig  `yaml:"limits"`
	Storage StorageConfig `yaml:"storage"`
	Models  []ModelConfig `yaml:"models"`
}

// Load reads, parses, and validates a config file at path. Fields not
// present in the file fall back to sane defaults from defaults().
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Port:                       8080,
			DefaultStrategy:            StrategyQueue,
			DefaultQueueTimeoutSeconds: 30,
		},
		Gemini: GeminiConfig{
			BaseURL:   "https://generativelanguage.googleapis.com",
			APIKeyEnv: "GEMINI_API_KEY",
		},
		Storage: StorageConfig{
			SQLitePath: "./data/gateway.db",
		},
	}
}

func (c *Config) validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("at least one model must be configured under 'models'")
	}

	seen := make(map[string]bool, len(c.Models))
	for i, m := range c.Models {
		if m.Name == "" {
			return fmt.Errorf("models[%d]: name is required", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("models[%d]: duplicate model name %q", i, m.Name)
		}
		seen[m.Name] = true
		if m.RPM <= 0 {
			return fmt.Errorf("model %q: rpm must be > 0", m.Name)
		}
		if m.TPM <= 0 {
			return fmt.Errorf("model %q: tpm must be > 0", m.Name)
		}
		if m.RPD <= 0 {
			return fmt.Errorf("model %q: rpd must be > 0", m.Name)
		}
	}

	if c.Server.DefaultStrategy != StrategyQueue && c.Server.DefaultStrategy != StrategyFailover {
		return fmt.Errorf("server.default_strategy must be %q or %q, got %q", StrategyQueue, StrategyFailover, c.Server.DefaultStrategy)
	}
	if c.Server.DefaultQueueTimeoutSeconds <= 0 {
		return fmt.Errorf("server.default_queue_timeout_seconds must be > 0")
	}

	if c.Gemini.APIKeyEnv == "" {
		return fmt.Errorf("gemini.api_key_env must be set")
	}
	if os.Getenv(c.Gemini.APIKeyEnv) == "" {
		return fmt.Errorf("environment variable %q (referenced by gemini.api_key_env) is not set", c.Gemini.APIKeyEnv)
	}

	return nil
}

// APIKey returns the Gemini API key read from the environment variable
// named in Gemini.APIKeyEnv. Keeping the key out of the config file
// avoids accidentally committing it.
func (c *Config) APIKey() string {
	return os.Getenv(c.Gemini.APIKeyEnv)
}
