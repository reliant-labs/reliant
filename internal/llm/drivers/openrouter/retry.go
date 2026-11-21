// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// Every OpenRouter request opens through the shared ladder
// (ratelimit.Retryer): the raw-HTTP paths below (Anthropic cache control,
// Gemini reasoning details) and the OpenAI client the other models use, which
// is given describeOpenRouter so both paths read a refusal the same way.

// retryer is the raw-HTTP paths' ladder.
func (c *Client) retryer() ratelimit.Retryer {
	r := c.ladder
	if r.Provider == "" {
		r.Provider = "OpenRouter"
		r.Log = "OpenRouter request"
	}
	return r
}

// openRequest POSTs body to the chat completions endpoint through the ladder
// and returns the 2xx response with its body unread. emit, when non-nil,
// receives each wait before it is slept.
func (c *Client) openRequest(ctx context.Context, client *http.Client, body []byte, emit func(llm.RetryWait) error) (*http.Response, error) {
	return c.retryer().OpenHTTP(ctx, client.Do, func() (*http.Request, error) {
		req, err := c.newOpenRouterRequest(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		return req, nil
	}, openRouterFailure, emit)
}

// openRouterFailure describes a refused raw-HTTP request to the ladder.
func openRouterFailure(resp *http.Response, body []byte, err error) ratelimit.Failure {
	if resp == nil {
		return ratelimit.HTTPFailure(nil, fmt.Errorf("failed to send request: %w", err))
	}
	f := ratelimit.HTTPFailure(resp, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body)))
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		describeOpenRouter(&f, env.Error)
	}
	return f
}

// describeOpenRouterAPIError is the OpenAI client's DescribeFailure hook.
func describeOpenRouterAPIError(f *ratelimit.Failure, apiErr *openai.Error) {
	describeOpenRouter(f, json.RawMessage(apiErr.RawJSON()))
}

// describeOpenRouter adds what OpenRouter's error object says. It sends no
// Retry-After for its own limits, only X-RateLimit-Reset (an epoch in ms),
// read as a hint. A daily limit (the free models' "free-models-per-day", or an
// upstream's per-day quota relayed in metadata.raw) lifts at that reset, hours
// away, so it ends the ladder as a usage window instead of eight futile
// retries.
func describeOpenRouter(f *ratelimit.Failure, errObj json.RawMessage) {
	f.ResetHint = max(f.ResetHint, ratelimit.ResetHint(f.Header, time.Now()))
	if f.Status != http.StatusTooManyRequests || len(errObj) == 0 {
		return
	}
	var e struct {
		Message  string `json:"message"`
		Metadata struct {
			Raw json.RawMessage `json:"raw"`
		} `json:"metadata"`
	}
	if json.Unmarshal(errObj, &e) != nil {
		return
	}
	text := strings.ToLower(e.Message + " " + string(e.Metadata.Raw))
	if !strings.Contains(text, "per-day") && !strings.Contains(text, "per day") && !strings.Contains(text, "perday") {
		return
	}
	f.UsageLimit = true
	f.Limit.Window = "daily"
	if t, ok := ratelimit.ParseResetTime(f.Header.Get("X-RateLimit-Reset")); ok {
		f.Limit.ResetAt = t
	}
}
