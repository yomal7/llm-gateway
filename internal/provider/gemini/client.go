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

func (c *Client) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, req.Model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
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

	return provider.GenerateResponse{
		Body:      respBody,
		ModelUsed: req.Model,
		Usage:     extractUsage(respBody),
	}, nil
}

func extractUsage(body []byte) provider.Usage {
	var parsed struct {
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return provider.Usage{}
	}
	return provider.Usage{
		InputTokens:  parsed.UsageMetadata.PromptTokenCount,
		OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
	}
}

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
	case http.StatusNotFound:
		kind = provider.ErrKindNotFound
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