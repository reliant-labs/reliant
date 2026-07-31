// Copyright (c) 2025 Reliant Labs
package activities

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// TestStreamIdleTimeoutIsRetryable pins the other half of the stream-idle fix.
//
// Cutting a silent stream is only an improvement if the cut is retried: the
// stalls it replaces were transient (is_terminal=false) and their automatic
// retry succeeded on the first attempt in 12-17s. autoClassify decides that by
// scanning the error text, terminal patterns first, so llm.ErrStreamIdleTimeout
// must miss every terminal pattern and hit a transient one. Reword it and this
// test tells you that you turned a recoverable stall into a failed workflow.
func TestStreamIdleTimeoutIsRetryable(t *testing.T) {
	// Wrapped exactly as call_llm.go reports it.
	wrapped := fmt.Errorf("failed to stream LLM response: %w", llm.ErrStreamIdleTimeout)

	for name, err := range map[string]error{
		"bare":    llm.ErrStreamIdleTimeout,
		"wrapped": wrapped,
	} {
		t.Run(name, func(t *testing.T) {
			classified := ClassifyError(err)
			require.Error(t, classified)

			// A retryable classification is the plain error; a terminal one is a
			// non-retryable temporal.ApplicationError.
			var appErr *temporal.ApplicationError
			assert.NotErrorAs(t, classified, &appErr,
				"a silent stream must be retried, not turned into a non-retryable failure")
			assert.False(t, IsTerminal(classified))
			assert.ErrorIs(t, classified, llm.ErrStreamIdleTimeout,
				"classification must not lose the cause")
			assert.Equal(t, ErrorCategoryNetwork, CategorizeError(err),
				"an idle stream is a network fault and should be reported as one")
		})
	}
}
