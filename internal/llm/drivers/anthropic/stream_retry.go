// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// The streaming request retries through ratelimit.Retryer, not the SDK (whose
// own retries base.go turns off): see the ladder's header comment for why. This
// file only translates an SDK error into the ladder's Failure.

// anthropicFailure describes a failed attempt to the shared ladder.
//
// A usage window is detected from the EXPLICIT signal first: a 429 whose
// `anthropic-ratelimit-unified-status` is `rejected`, or whose body names a
// usage limit. The ladder's Retry-After-longer-than-5-minutes rule remains as
// the fallback for responses that say nothing explicit.
func anthropicFailure(err error) ratelimit.Failure {
	f := ratelimit.Failure{Err: err}
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return f
	}
	f.Status = apiErr.StatusCode
	if apiErr.Response != nil {
		f.Header = apiErr.Response.Header
	}
	info, explicit := ratelimit.AnthropicLimitInfo(f.Header)
	f.Limit = info
	if apiErr.StatusCode == http.StatusTooManyRequests {
		errType, msg := errorTypeAndMessage(apiErr.RawJSON())
		f.UsageLimit = explicit || ratelimit.AnthropicBodyIsUsageLimit(errType, msg)
	}
	return f
}

// errorTypeAndMessage reads {"type":"error","error":{"type":..,"message":..}}
// (or the unwrapped inner object) defensively.
func errorTypeAndMessage(raw string) (errType, msg string) {
	var body struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return "", ""
	}
	if body.Error != nil {
		return body.Error.Type, body.Error.Message
	}
	return body.Type, body.Message
}
