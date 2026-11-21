// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"google.golang.org/genai"
)

// Both Vertex backends open their request through the shared ladder
// (ratelimit.Retryer), as every driver does: 408/409/429/5xx and dropped
// connections retry, the provider's wait is honored up to the ladder's cap,
// and a streaming wait is published as EventRetryWait before it is slept.
// Vertex states its wait in a google.rpc.RetryInfo, not a Retry-After header,
// for Claude's quota refusals as well as Gemini's; DescribeGoogle reads it.

func (c *VertexAIClient) retryer() ratelimit.Retryer {
	r := c.ladder
	if r.Provider == "" {
		r.Provider = "Vertex AI"
		r.Log = "Vertex AI request"
	}
	return r
}

// geminiFailure describes a failed genai call to the ladder. The status comes
// from genai.APIError (or, for an error sent in-band, its status name); a
// status-less error is retried only when it is a transport failure.
func geminiFailure(err error) ratelimit.Failure {
	f := ratelimit.Failure{Err: err}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		f.Fatal = !ratelimit.IsTransportError(err)
		return f
	}
	f.Status = ratelimit.GoogleHTTPStatus(apiErr.Code, apiErr.Status)
	f.Fatal = f.Status == 0
	ratelimit.DescribeGoogle(&f, apiErr.Details)
	return f
}

// claudeFailure describes a refused Vertex Claude request. A refusal from
// Vertex itself (quota, auth) is a google.rpc.Status; one from the model is
// Anthropic's {"type":"error","error":{...}}, whose usage-window wording ends
// the ladder as it does on the Anthropic driver.
func claudeFailure(resp *http.Response, body []byte, err error) ratelimit.Failure {
	if resp == nil {
		return ratelimit.HTTPFailure(nil, fmt.Errorf("request failed: %w", err))
	}
	f := ratelimit.HTTPFailure(resp, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body)))
	if s, ok := ratelimit.ParseGoogleStatus(body); ok {
		ratelimit.DescribeGoogle(&f, s.Details)
		return f
	}
	var anthropicErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &anthropicErr) == nil && resp.StatusCode == http.StatusTooManyRequests {
		f.UsageLimit = ratelimit.AnthropicBodyIsUsageLimit(anthropicErr.Error.Type, anthropicErr.Error.Message)
	}
	return f
}
