// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// The retry class of each failure stop is its identity: the malformed-output
// stops are retried once by CallLLM, a missing signature by signature
// recovery, and nothing else is retried at all.
func TestStopFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason    string
		malformed bool
		signature bool
	}{
		{"MALFORMED_FUNCTION_CALL", true, false},
		{"UNEXPECTED_TOOL_CALL", true, false},
		{"MALFORMED_RESPONSE", true, false},
		{"MISSING_THOUGHT_SIGNATURE", false, true},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			err := StopFailure(SourceGemini, tc.reason, "call: default_api:bash{")
			require.Error(t, err)
			// Wrapped, as a driver's caller reports it.
			wrapped := fmt.Errorf("LLM streaming error: %w", err)
			assert.Equal(t, tc.malformed, errors.Is(wrapped, drivererrors.ErrMalformedModelOutput))
			assert.Equal(t, tc.signature, errors.Is(wrapped, ErrThoughtSignatureRejected))
			assert.Equal(t, tc.signature, IsThoughtSignatureRejection(wrapped))
			assert.Equal(t, "Gemini stopped with "+tc.reason+": call: default_api:bash{", err.Error())
		})
	}

	for _, reason := range []string{"STOP", "MAX_TOKENS", "SAFETY", "TOO_MANY_TOOL_CALLS", "NO_IMAGE", "", "OTHER"} {
		assert.NoError(t, StopFailure(SourceGemini, reason, ""), reason)
	}
}

// A StopError for a reason that is not retried (vertexai builds one for every
// error reason) matches neither retry class.
func TestStopError_NonRetryableReasons(t *testing.T) {
	t.Parallel()
	err := &StopError{Source: SourceVertexAI, Reason: "SAFETY"}
	assert.False(t, errors.Is(err, drivererrors.ErrMalformedModelOutput))
	assert.False(t, IsThoughtSignatureRejection(err))
	assert.Equal(t, "Gemini stopped with SAFETY", err.Error())
}
