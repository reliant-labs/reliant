// Copyright (c) 2025 Reliant Labs
package codex

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// retryer returns the Codex request's retry ladder: the same shared ladder the
// Anthropic drivers use (ratelimit.Retryer), so every wait is published as
// EventRetryWait and recorded as provider_backoff.
func (c *CodexClient) retryer() ratelimit.Retryer {
	r := c.ladder
	if r.Provider == "" {
		r.Provider = "Codex"
		r.Log = "Codex request"
	}
	return r
}

// codexFailure describes a failed attempt to the shared ladder.
//
// A 429 is a usage window — and ends the ladder at once with the reset time —
// when its body is codex-rs's typed `usage_limit_reached` error, or when the
// x-codex-* headers show an exhausted window / name a reached limit. Quota
// exhaustion (usage_not_included, insufficient_quota, spend limits) is fatal:
// retrying cannot help and there is no reset to wait for.
// Verified: codex-rs codex-api/src/api_bridge.rs map_api_error (HTTP 429 arm).
func codexFailure(err error) ratelimit.Failure {
	f := ratelimit.Failure{Err: err}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return f
	}
	f.Status = apiErr.StatusCode
	if apiErr.Response != nil {
		f.Header = apiErr.Response.Header
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		return f
	}
	body := errorBody(apiErr)
	now := time.Now()
	if info, ok := ratelimit.CodexUsageLimit(body, f.Header, now); ok {
		f.UsageLimit, f.Limit = true, info
	} else if ratelimit.CodexQuotaExhausted(body) {
		f.Fatal = true
	} else if info, ok := ratelimit.CodexHeaderLimit(f.Header, now); ok {
		f.UsageLimit, f.Limit = true, info
	}
	return f
}

// errorBody returns the response body the SDK read for apiErr: its decoded
// "error" object when present, else the raw body the SDK left on the response.
func errorBody(apiErr *openai.Error) []byte {
	if raw := apiErr.RawJSON(); raw != "" {
		return []byte(raw)
	}
	if apiErr.Response == nil || apiErr.Response.Body == nil {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(apiErr.Response.Body, 8192))
	_ = apiErr.Response.Body.Close()
	apiErr.Response.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}
