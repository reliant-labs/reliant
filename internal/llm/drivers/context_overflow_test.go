// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

func anthropicError(t *testing.T, status int, body string) error {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	e := &anthropic.Error{}
	require.NoError(t, e.UnmarshalJSON([]byte(body)))
	e.StatusCode, e.Request, e.Response = status, req, &http.Response{StatusCode: status}
	return e
}

func openaiError(t *testing.T, status int, body string) error {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	e := &openai.Error{}
	require.NoError(t, e.UnmarshalJSON([]byte(body)))
	e.StatusCode, e.Request, e.Response = status, req, &http.Response{StatusCode: status}
	return e
}

// The SDK error types carry the status and the provider's code; the marking
// reads them before any wording, so a 429 is never an overflow even when its
// body would read like one, and a code alone is enough.
func TestMarkContextOverflow_SDKErrors(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"anthropic prompt too long": {anthropicError(t, 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`), true},
		"anthropic request_too_large by type": {anthropicError(t, 413,
			`{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`), true},
		"anthropic 429 whose body mentions a long prompt": {anthropicError(t, 429,
			`{"type":"error","error":{"type":"rate_limit_error","message":"prompt is too long for your remaining input tokens"}}`), false},
		"openai context_length_exceeded code": {openaiError(t, 400,
			`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`), true},
		"openai code with reworded message": {openaiError(t, 400,
			`{"error":{"message":"request rejected","type":"invalid_request_error","code":"context_length_exceeded"}}`), true},
		"openai 429": {openaiError(t, 429,
			`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`), false},
		"llama.cpp exceed_context_size_error type": {openaiError(t, 400,
			`{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`), true},
		"gemini overflow": {genai.APIError{Code: 400, Status: "INVALID_ARGUMENT",
			Message: "The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."}, true},
		"gemini 429 by pointer": {&genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED",
			Message: "The input token count exceeds the maximum number of tokens allowed"}, false},
		"openrouter text status": {errors.New(`API request failed with status 400: {"error":{"message":"This endpoint's maximum context length is 200000 tokens. However, you requested about 234543 tokens."}}`), true},
		"openrouter text 429":    {errors.New(`API request failed with status 429: {"error":{"message":"maximum context length is 200000 tokens"}}`), false},
		"wrapped sdk error":      {fmt.Errorf("streaming failed: %w", openaiError(t, 400, `{"error":{"message":"x","code":"context_length_exceeded"}}`)), true},
		"nil":                    {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			marked := markContextOverflow(tc.err)
			assert.Equal(t, tc.want, errors.Is(marked, drivererrors.ErrContextOverflow))
			if tc.err != nil {
				assert.Equal(t, tc.err.Error(), marked.Error(), "marking never changes the text")
				// genai.APIError is a non-comparable value, which errors.Is cannot match.
				if reflect.TypeOf(tc.err).Comparable() {
					assert.ErrorIs(t, marked, tc.err)
				}
			}
		})
	}
}

// Every production driver streams through baseDriver, so the overflow is
// marked there once for all of them, on the stream and on SendMessages.
func TestBaseDriver_MarksContextOverflow(t *testing.T) {
	t.Parallel()
	refusal := anthropicError(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`)
	d := &baseDriver{client: &scriptedClient{events: []llm.DriverEvent{{Type: llm.EventError, Error: refusal}}}}

	var got error
	for ev := range d.StreamResponse(context.Background(), nil, nil, nil) {
		if ev.Type == llm.EventError {
			got = ev.Error
		}
	}
	require.Error(t, got)
	assert.ErrorIs(t, got, drivererrors.ErrContextOverflow)
	var sdkErr *anthropic.Error
	assert.ErrorAs(t, got, &sdkErr, "the SDK error stays reachable for the other classifiers")

	_, err := d.SendMessages(context.Background(), nil, nil, nil)
	assert.ErrorIs(t, err, drivererrors.ErrContextOverflow)
}
