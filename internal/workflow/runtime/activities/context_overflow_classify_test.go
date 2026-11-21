// Copyright (c) 2025 Reliant Labs
package activities

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// A context-window refusal that reaches the activity boundary was already
// resent smaller by call_llm, so it is not retried, and it keeps its type
// across Temporal (decided by identity, as in the runtime's classifyError).
func TestContextOverflowIsTerminal(t *testing.T) {
	t.Parallel()
	refusal := &drivererrors.ContextOverflowError{Err: errors.New("prompt is too long: 204291 tokens > 200000 maximum")}
	err := fmt.Errorf("LLM streaming error: %w", refusal)

	classified := ClassifyError(err)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, classified, &appErr)
	assert.Equal(t, ContextOverflowErrType, appErr.Type())
	assert.True(t, appErr.NonRetryable())
	assert.True(t, IsTerminal(err))
	assert.True(t, IsTerminal(classified))
	assert.ErrorIs(t, classified, drivererrors.ErrContextOverflow)
	assert.Equal(t, ErrorCategoryTerminal, CategorizeError(err))
}
