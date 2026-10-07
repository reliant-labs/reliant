// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

func TestFailStepActivity(t *testing.T) {
	t.Run("always fails with provided error message", func(t *testing.T) {
		activity := NewFailStepActivity()
		ctx := context.Background()

		input := FailStepInput{
			Error: "Agent step has empty prompt",
		}

		_, err := activity.Execute(ctx, input)

		require.Error(t, err)
		assert.Equal(t, "workflow validation failed: Agent step has empty prompt", err.Error())
	})

	t.Run("is not retried: the same definition and inputs fail the same way", func(t *testing.T) {
		_, err := NewFailStepActivity().Execute(context.Background(), FailStepInput{Error: "no such key: channel"})

		var appErr *temporal.ApplicationError
		require.ErrorAs(t, err, &appErr)
		assert.True(t, appErr.NonRetryable())
	})

	t.Run("activity name is V2_FailStep", func(t *testing.T) {
		activity := NewFailStepActivity()
		assert.Equal(t, "FailStep", activity.Name())
	})

	t.Run("fails with custom error messages", func(t *testing.T) {
		activity := NewFailStepActivity()
		ctx := context.Background()

		testCases := []struct {
			name         string
			errorMessage string
		}{
			{
				name:         "empty prompt",
				errorMessage: "Agent step agent-123 has an empty prompt",
			},
			{
				name:         "missing prompt",
				errorMessage: "Agent step agent-456 is missing a prompt input",
			},
			{
				name:         "invalid workflow",
				errorMessage: "Workflow was created before validation was added",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				input := FailStepInput{Error: tc.errorMessage}
				_, err := activity.Execute(ctx, input)

				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMessage)
			})
		}
	})
}

// The step executor names the failing node in FailStep's input (step id,
// workflow, loop scope, node path) so the activity wrapper files the failure
// under that node. Temporal decodes the input into FailStepInput before the
// wrapper reads it, so a key the type has no field for never arrives.
func TestFailStepInput_KeepsTheNodeLocation(t *testing.T) {
	sent := map[string]interface{}{
		"chat_id":        "chat-1",
		"workflow_id":    "wf-1",
		"step_id":        "post",
		"error":          "CEL evaluation failed for step post: boom",
		"loop_node_id":   "attempt",
		"loop_iteration": 2,
		"node_path":      "attempt.post",
	}
	encoded, err := json.Marshal(sent)
	require.NoError(t, err)
	var delivered FailStepInput
	require.NoError(t, json.Unmarshal(encoded, &delivered))

	reencoded, err := json.Marshal(delivered)
	require.NoError(t, err)
	var seen map[string]interface{}
	require.NoError(t, json.Unmarshal(reencoded, &seen))
	for key, want := range sent {
		assert.EqualValues(t, want, seen[key], key)
	}
}
