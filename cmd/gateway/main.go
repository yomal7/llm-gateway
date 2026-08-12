package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	geminiapi "github.com/yomal7/llm-gateway/internal/api/gemini"
	"github.com/yomal7/llm-gateway/internal/config"
	"github.com/yomal7/llm-gateway/internal/httplog"
	"github.com/yomal7/llm-gateway/internal/limiter"
	"github.com/yomal7/llm-gateway/internal/provider/gemini"
	"github.com/yomal7/llm-gateway/internal/scheduler"
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

	geminiClient := gemini.New(cfg.Gemini.BaseURL, cfg.APIKey())

	limiterModels := make([]limiter.Model, len(cfg.Models))
	for i, m := range cfg.Models {
		limiterModels[i] = limiter.Model{Name: m.Name, RPM: m.RPM, TPM: m.TPM, RPD: m.RPD}
	}

	sched := scheduler.New(
		geminiClient,
		limiterModels,
		scheduler.Strategy(cfg.Server.DefaultStrategy),
		time.Duration(cfg.Server.DefaultQueueTimeoutSeconds)*time.Second,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","models_configured":%d}`, len(cfg.Models))
	})
	geminiapi.RegisterRoutes(mux, sched)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	slog.Info("gateway listening", "addr", addr)
	if err := http.ListenAndServe(addr, httplog.Middleware(mux)); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}