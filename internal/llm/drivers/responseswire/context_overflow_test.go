// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

func decodeResponse(t *testing.T, raw string) *responses.Response {
	t.Helper()
	var resp responses.Response
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	return &resp
}

// The Codex backend's refusal of an oversized prompt, verbatim from codex-rs
// (codex-api/src/sse/responses.rs, the test that maps it to
// ApiError::ContextWindowExceeded).
const codexContextWindowFailed = `{"id":"resp_5c66275b97b9baef1ed95550adb3b7ec13b17aafd1d2f11b","object":"response","created_at":1759510079,"status":"failed","background":false,"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."},"usage":null,"user":null,"metadata":{}}`

func TestContextOverflowFailure(t *testing.T) {
	t.Parallel()

	err := ContextOverflowFailure(decodeResponse(t, codexContextWindowFailed))
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrContextOverflow)
	assert.Contains(t, err.Error(), "context_length_exceeded")
	assert.Contains(t, err.Error(), "exceeds the context window")

	for name, raw := range map[string]string{
		"failed for another reason": `{"id":"r","status":"failed","error":{"code":"server_error","message":"The server had an error processing your request."}}`,
		"failed with a rate limit":  `{"id":"r","status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit reached for tokens per min"}}`,
		"failed with no error":      `{"id":"r","status":"failed"}`,
		"completed":                 `{"id":"r","status":"completed"}`,
		"incomplete":                `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, ContextOverflowFailure(decodeResponse(t, raw)))
		})
	}
	assert.NoError(t, ContextOverflowFailure(nil))
}
