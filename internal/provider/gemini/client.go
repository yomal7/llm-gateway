// Package gemini implements provider.Provider against Google's Gemini
// API. The request path mirrors Gemini's real REST shape
// (/v1beta/models/{model}:generateContent) on purpose: code already
// written against the official Gemini SDK/REST API can point at this
// gateway just by swapping the base URL.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yomal7/llm-gateway/internal/provider"
)

const defaultTimeout = 60 * time.Second

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

func (c *Client) Name() string { return "gemini" }

// --- wire types: these mirror Gemini's actual REST request/response shapes ---

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type generateContentRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type candidate struct {
	Content geminiContent `json:"content"`
}

type generateContentResponse struct {
	Candidates    []candidate   `json:"candidates"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
}

// errorDetail models one entry of Gemini's error.details array. On
// RESOURCE_EXHAUSTED (429) responses this is where the retryDelay
// google.rpc.RetryInfo detail lives — the same field visible in the
// 429 errors from your triage-tool logs.
type errorDetail struct {
	Type       string `json:"@type"`
	RetryDelay string `json:"retryDelay"`
}

type errorBody struct {
	Error struct {
		Code    int           `json:"code"`
		Message string        `json:"message"`
		Status  string        `json:"status"`
		Details []errorDetail `json:"details"`
	} `json:"error"`
}

func (c *Client) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	body := generateContentRequest{
		GenerationConfig: generationConfig{
			MaxOutputTokens: req.MaxOutputTokens,
			Temperature:     req.Temperature,
		},
	}
	for _, content := range req.Contents {
		body.Contents = append(body.Contents, geminiContent{
			Role:  content.Role,
			Parts: []geminiPart{{Text: content.Text}},
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("marshaling gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, req.Model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("building gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("calling gemini: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("reading gemini response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return provider.GenerateResponse{}, classifyError(resp.StatusCode, respBody)
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("parsing gemini response: %w", err)
	}

	var text strings.Builder
	if len(parsed.Candidates) > 0 {
		for _, part := range parsed.Candidates[0].Content.Parts {
			text.WriteString(part.Text)
		}
	}

	return provider.GenerateResponse{
		Text:      text.String(),
		ModelUsed: req.Model,
		Usage: provider.Usage{
			InputTokens:  parsed.UsageMetadata.PromptTokenCount,
			OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}

// classifyError turns Gemini's HTTP status and error body into a
// provider.Error, extracting the RetryInfo delay when Google supplies
// one so the scheduler can back off intelligently instead of guessing.
func classifyError(statusCode int, body []byte) *provider.Error {
	var parsed errorBody
	message := string(body)
	var retryAfter time.Duration

	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
		for _, d := range parsed.Error.Details {
			if strings.Contains(d.Type, "RetryInfo") && d.RetryDelay != "" {
				if dur, err := time.ParseDuration(d.RetryDelay); err == nil {
					retryAfter = dur
				}
			}
		}
	}

	kind := provider.ErrKindUnknown
	switch statusCode {
	case http.StatusTooManyRequests:
		kind = provider.ErrKindRateLimited
	case http.StatusServiceUnavailable:
		kind = provider.ErrKindUnavailable
	case http.StatusBadRequest:
		kind = provider.ErrKindInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = provider.ErrKindAuth
	}

	return &provider.Error{
		Kind:       kind,
		StatusCode: statusCode,
		Message:    message,
		RetryAfter: retryAfter,
	}
}
