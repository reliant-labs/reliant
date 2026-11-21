// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// A context-window refusal reaching the activity boundary has already been
// resent smaller by call_llm, so it is terminal; and it is decided by
// identity, before the text rules. The "429 in a token count" case is the
// point: the streaming rule treats "429" anywhere in a "failed to stream"
// message as a transient status, and a token count can contain those digits.
// What crosses to the workflow keeps the type, since Go identity does not
// survive serialization.
func TestClassifyError_ContextOverflowIsTerminalByIdentity(t *testing.T) {
	t.Parallel()
	refusal := &drivererrors.ContextOverflowError{
		Err: errors.New(`400 Bad Request {"error":{"type":"invalid_request_error","message":"prompt is too long: 204291 tokens > 200000 maximum"}}`),
	}
	for name, err := range map[string]error{
		"bare":                   refusal,
		"wrapped":                fmt.Errorf("LLM streaming error: %w", refusal),
		"429 in a token count":   fmt.Errorf("failed to stream: %w", refusal),
		"call_llm's explanation": fmt.Errorf("the conversation is too long for claude-test: ... Start a new chat: %w", refusal),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			classified := classifyError(err)
			assert.True(t, isTerminal(err), "the raw error reads as terminal")
			assert.True(t, isTerminal(classified))
			assert.ErrorIs(t, classified, drivererrors.ErrContextOverflow, "classification keeps the cause")
			assert.Equal(t, "terminal", categorizeError(err))

			var appErr *temporal.ApplicationError
			require.ErrorAs(t, flattenForTemporal(classified), &appErr)
			assert.Equal(t, ContextOverflowErrType, appErr.Type())
			assert.True(t, appErr.NonRetryable())
		})
	}

	// The control: without the identity, the "429" case reads as a transient
	// streaming failure, so the case above is identity winning.
	assert.False(t, isTerminal(classifyError(errors.New("failed to stream: 204291 tokens"))))
}

// An overflow that outlived call_llm's resends is the user's to change: a user
// error (INFO, never Sentry), with its text and identity unchanged.
func TestMarkTextOnlyUserError_ContextOverflow(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("LLM streaming error: %w", &drivererrors.ContextOverflowError{Err: errors.New("prompt is too long")})
	marked := markTextOnlyUserError(err)
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(marked))
	assert.ErrorIs(t, marked, drivererrors.ErrContextOverflow)
	assert.Equal(t, err.Error(), marked.Error())
}
