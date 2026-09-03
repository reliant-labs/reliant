// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStepEvent_RetryExhausted_FlagBehavior verifies that StepEvents with
// RetryExhausted=true are handled correctly by EnsureStepEventRoutable.
// The RetryExhausted flag should NOT prevent routing — the caller is
// responsible for checking it before calling EnsureStepEventRoutable.
func TestStepEvent_RetryExhausted_FlagBehavior(t *testing.T) {
	t.Parallel()
	t.Run("RetryExhausted event with error is routable error", func(t *testing.T) {
		event := &StepEvent{
			StepID:         "call_llm",
			Error:          errors.New("429 Too Many Requests"),
			RetryExhausted: true,
		}

		err := EnsureStepEventRoutable(event)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "429 Too Many Requests")
	})

	t.Run("RetryExhausted flag is accessible on step event", func(t *testing.T) {
		event := &StepEvent{
			StepID:         "call_llm",
			Error:          errors.New("rate limit exceeded"),
			RetryExhausted: true,
		}
		assert.True(t, event.RetryExhausted)
	})

	t.Run("non-exhausted event has flag false", func(t *testing.T) {
		event := &StepEvent{
			StepID: "call_llm",
			Data:   map[string]interface{}{"response_text": "hello"},
		}
		assert.False(t, event.RetryExhausted)
	})

	t.Run("ToEvent preserves error info for RetryExhausted events", func(t *testing.T) {
		event := &StepEvent{
			ID:             "evt-1",
			WorkflowID:     "wf-1",
			ChatID:         "chat-1",
			WorkflowName:   "test",
			StepID:         "call_llm",
			Error:          errors.New("429 rate limit"),
			RetryExhausted: true,
		}

		wfEvent := event.ToEvent()
		require.NotNil(t, wfEvent)
		assert.Equal(t, "call_llm", wfEvent.StepID)
		require.Contains(t, wfEvent.Data, "error")
		assert.Contains(t, wfEvent.Data["error"], "429 rate limit")
	})
}

// TestPauseController_RequestPause verifies the RequestPause callback works
// and is nil-safe when not set.
func TestPauseController_RequestPause(t *testing.T) {
	t.Parallel()
	t.Run("DoRequestPause with nil receiver", func(t *testing.T) {
		var pc *PauseController
		// Should not panic
		pc.DoRequestPause()
	})

	t.Run("DoRequestPause with nil function", func(t *testing.T) {
		pc := &PauseController{}
		// Should not panic
		pc.DoRequestPause()
	})

	t.Run("DoRequestPause calls the function", func(t *testing.T) {
		called := false
		pc := &PauseController{
			RequestPause: func() { called = true },
		}
		pc.DoRequestPause()
		assert.True(t, called)
	})
}
