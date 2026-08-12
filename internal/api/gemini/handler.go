package geminiapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yomal7/llm-gateway/internal/provider"
	"github.com/yomal7/llm-gateway/internal/scheduler"
)

type errorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func RegisterRoutes(mux *http.ServeMux, sched *scheduler.Scheduler) {
	mux.HandleFunc("POST /v1beta/models/{modelAndAction}", func(w http.ResponseWriter, r *http.Request) {
		_, action, ok := strings.Cut(r.PathValue("modelAndAction"), ":")
		if !ok || action != "generateContent" {
			writeError(w, http.StatusNotFound, "unsupported path — expected /v1beta/models/{model}:generateContent", "NOT_FOUND")
			return
		}

		strategy, ok := parseStrategy(r.Header.Get("X-Gateway-Strategy"))
		if !ok {
			writeError(w, http.StatusBadRequest, `X-Gateway-Strategy must be "queue" or "failover" if set`, "INVALID_ARGUMENT")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading request body: "+err.Error(), "INVALID_ARGUMENT")
			return
		}
		if !json.Valid(body) {
			writeError(w, http.StatusBadRequest, "request body is not valid JSON", "INVALID_ARGUMENT")
			return
		}

		start := time.Now()
		result, genErr := sched.Generate(r.Context(), scheduler.Request{
			Body:     body,
			Strategy: strategy,
		})

		status := http.StatusOK
		respMessage := ""
		if genErr != nil {
			status, respMessage = schedulerErrorStatus(genErr)
		}
		logGenerateAttempt(start, strategy, status, result, genErr)

		if genErr != nil {
			writeError(w, status, respMessage, "")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Model-Used", result.ModelUsed)
		_, _ = w.Write(result.Response.Body) // forward the provider's response verbatim
	})
}

func logGenerateAttempt(start time.Time, strategy scheduler.Strategy, status int, result scheduler.Result, genErr error) {
	strategyLabel := string(strategy)
	if strategyLabel == "" {
		strategyLabel = "default"
	}

	attrs := []any{
		"status", status,
		"model_used", result.ModelUsed,
		"strategy", strategyLabel,
		"attempts", attemptSummaries(result.Attempts),
		"duration_ms", time.Since(start).Milliseconds(),
	}

	if genErr != nil {
		slog.Warn("generate_content", append(attrs, "error", genErr.Error())...)
		return
	}
	slog.Info("generate_content", attrs...)
}

func attemptSummaries(attempts []scheduler.Attempt) []string {
	out := make([]string, len(attempts))
	for i, a := range attempts {
		switch {
		case a.Outcome == "success":
			out[i] = a.Model + ":success"
		case a.Reason != "":
			out[i] = a.Model + ":" + a.Outcome + ":" + a.Reason
		case a.Err != nil:
			out[i] = a.Model + ":" + a.Outcome + ":" + a.Err.Error()
		default:
			out[i] = a.Model + ":" + a.Outcome
		}
	}
	return out
}

func parseStrategy(header string) (scheduler.Strategy, bool) {
	switch header {
	case "":
		return "", true
	case string(scheduler.StrategyQueue):
		return scheduler.StrategyQueue, true
	case string(scheduler.StrategyFailover):
		return scheduler.StrategyFailover, true
	default:
		return "", false
	}
}

func writeError(w http.ResponseWriter, status int, message, statusText string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var body errorResponse
	body.Error.Code = status
	body.Error.Message = message
	body.Error.Status = statusText
	_ = json.NewEncoder(w).Encode(body)
}

func schedulerErrorStatus(err error) (status int, message string) {
	var provErr *provider.Error
	if errors.As(err, &provErr) {
		return provErr.StatusCode, provErr.Message
	}
	return http.StatusServiceUnavailable, err.Error()
}