// Command gateway is the entrypoint for the LLM gateway. At this stage
// (M0/M1) it loads config, sets up logging, constructs the Gemini
// client, and serves a health endpoint. The Gateway API routes and
// scheduler land in M3-M5.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/yomal7/llm-gateway/internal/config"
	"github.com/yomal7/llm-gateway/internal/provider/gemini"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to the gateway config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err, "path", *configPath)
		os.Exit(1)
	}
	slog.Info("config loaded",
		"models_configured", len(cfg.Models),
		"port", cfg.Server.Port,
		"default_strategy", cfg.Server.DefaultStrategy,
	)

	// Constructed here so startup fails fast on obvious misconfiguration.
	// Not wired to any route yet — the scheduler (M3) and API adapters
	// (M4/M5) are what will actually call Generate().
	_ = gemini.New(cfg.Gemini.BaseURL, cfg.APIKey())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","models_configured":%d}`, len(cfg.Models))
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	slog.Info("gateway listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
