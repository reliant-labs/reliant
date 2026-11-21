// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// Malformed model output is retried once, and only by CallLLM's own
// schedule (handlers.malformedOutputError, a selfSchedulingRetry). A copy
// that reaches the classifier without that wrapper must be terminal, never
// left to the step's blanket retry policy: a model that keeps producing the
// same malformed call would otherwise be asked again five times. The
// sentinel's own words ("malformed") make it so.
func TestClassifyError_UnscheduledMalformedOutputIsTerminal(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("failed to stream LLM response: LLM streaming error: Gemini stopped with MALFORMED_FUNCTION_CALL: %w",
		drivererrors.ErrMalformedModelOutput)
	assert.True(t, isTerminal(classifyError(err)))
}
