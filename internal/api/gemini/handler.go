package geminiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yomal7/llm-gateway/internal/provider"
	"github.com/yomal7/llm-gateway/internal/scheduler"
)


type part struct {
	Text string `json:"text"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type generateRequest struct {
	Contents         []content        `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type candidate struct {
	Content content `json:"content"`
}

type generateResponse struct {
	Candidates    []candidate   `json:"candidates"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
}

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
			writeError(w, http.StatusBadRequest, "X-Gateway-Strategy must be \"queue\" or \"failover\" if set", "INVALID_ARGUMENT")
			return
		}

		var body generateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "INVALID_ARGUMENT")
			return
		}

		result, err := sched.Generate(r.Context(), scheduler.Request{
			Contents:        toProviderContents(body.Contents),
			MaxOutputTokens: body.GenerationConfig.MaxOutputTokens,
			Temperature:     body.GenerationConfig.Temperature,
			Strategy:        strategy,
		})
		if err != nil {
			writeSchedulerError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Model-Used", result.ModelUsed)
		_ = json.NewEncoder(w).Encode(generateResponse{
			Candidates: []candidate{{
				Content: content{Role: "model", Parts: []part{{Text: result.Response.Text}}},
			}},
			UsageMetadata: usageMetadata{
				PromptTokenCount:     result.Response.Usage.InputTokens,
				CandidatesTokenCount: result.Response.Usage.OutputTokens,
				TotalTokenCount:      result.Response.Usage.InputTokens + result.Response.Usage.OutputTokens,
			},
		})
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

func toProviderContents(cs []content) []provider.Content {
	out := make([]provider.Content, 0, len(cs))
	for _, c := range cs {
		var text strings.Builder
		for _, p := range c.Parts {
			text.WriteString(p.Text)
		}
		out = append(out, provider.Content{Role: c.Role, Text: text.String()})
	}
	return out
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