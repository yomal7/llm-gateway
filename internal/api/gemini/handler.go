package geminiapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

		result, err := sched.Generate(r.Context(), scheduler.Request{
			Body:     body,
			Strategy: strategy,
		})
		if err != nil {
			writeSchedulerError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Model-Used", result.ModelUsed)
		_, _ = w.Write(result.Response.Body)
	})
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

func writeSchedulerError(w http.ResponseWriter, err error) {
	var provErr *provider.Error
	if errors.As(err, &provErr) {
		writeError(w, provErr.StatusCode, provErr.Message, "")
		return
	}
	writeError(w, http.StatusServiceUnavailable, err.Error(), "UNAVAILABLE")
}