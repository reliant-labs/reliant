// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// A provider stream that closed before its terminal event (a Responses stream
// with no response.completed) is retried, decided by the error's identity and
// not its words. The "terminal words" case is the point: wrapping layers add
// text, and "not found" / "invalid" are terminal text rules that run after the
// identity check. The classification happens here, in the worker, where the Go
// error still has its identity; what crosses the activity boundary is the
// flattened ApplicationError, which must keep the type and stay retryable.
func TestClassifyError_StreamEndedEarlyIsRetryableByIdentity(t *testing.T) {
	t.Parallel()
	// Shaped like what call_llm returns: its handleError prefix around the
	// Responses driver's error.
	driverErr := fmt.Errorf("responses stream closed before a terminal event (response.completed): %w",
		drivererrors.ErrStreamEndedEarly)

	for name, err := range map[string]error{
		"bare":    drivererrors.ErrStreamEndedEarly,
		"wrapped": fmt.Errorf("LLM streaming error: %w", driverErr),
		"wrapped with terminal words": fmt.Errorf("thread not found in cache; invalid partial response: %w",
			fmt.Errorf("LLM streaming error: %w", driverErr)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			classified := classifyError(err)
			require.Error(t, classified)
			assert.False(t, isTerminal(err), "the raw error must not read as terminal: %v", err)
			assert.False(t, isTerminal(classified), "a stream cut short must be retried: %v", classified)
			assert.ErrorIs(t, classified, drivererrors.ErrStreamEndedEarly, "classification must keep the cause")
			assert.Equal(t, "network", categorizeError(err))

			var appErr *temporal.ApplicationError
			require.ErrorAs(t, flattenForTemporal(classified), &appErr)
			assert.Equal(t, StreamEndedEarlyErrType, appErr.Type(),
				"the workflow side sees the type, since Go identity does not survive serialization")
			assert.False(t, appErr.NonRetryable())
		})
	}

	// The control: the same terminal words without the sentinel are terminal,
	// so the case above proves identity won rather than that the words are
	// harmless.
	assert.True(t, isTerminal(classifyError(fmt.Errorf("thread not found in cache; invalid partial response"))))
}
