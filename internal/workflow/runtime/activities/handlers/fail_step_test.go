// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		assert.Contains(t, err.Error(), "workflow validation failed")
		assert.Contains(t, err.Error(), "Agent step has empty prompt")
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
