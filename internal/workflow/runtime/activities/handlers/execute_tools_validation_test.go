// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TOOL CALL VALIDATION TESTS
// ============================================================================

// TestExecuteToolsActivity_ToolValidation tests that tool calls are validated
// against the AvailableTools list to prevent LLM hallucinations.
func TestExecuteToolsActivity_ToolValidation(t *testing.T) {
	t.Run("Tool not in AvailableTools returns error", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with a tool call that has AvailableTools set
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:             "call_hallucinated",
					Name:           "nonexistent_tool", // Tool not in AvailableTools
					Input:          `{"param": "value"}`,
					AvailableTools: []string{"bash", "view", "edit"}, // Allowed tools
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "not available")
		assert.Contains(t, output.ToolResults[0].Content, "hallucinated")

		// Tool should NOT have been executed
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_hallucinated"))
	})

	t.Run("Tool in AvailableTools executes normally", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with a valid tool call
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:             "call_valid",
					Name:           "bash",
					Input:          `{"command": "ls"}`,
					AvailableTools: []string{"bash", "view", "edit"},
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)

		// Tool should have been executed
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_valid"))
	})

	t.Run("Empty AvailableTools skips validation (backwards compat)", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with no AvailableTools (nil)
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:             "call_any_tool",
					Name:           "any_tool_name",
					Input:          `{}`,
					AvailableTools: nil, // No validation
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify - tool should be executed (passes validation)
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		// The tool may fail for other reasons (not found in registry),
		// but it should NOT fail due to AvailableTools validation
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_any_tool"))
	})
}

// TestExecuteToolsActivity_SpawnPresetValidation tests that spawn tool calls
// are validated against the AvailablePresets list.
func TestExecuteToolsActivity_SpawnPresetValidation(t *testing.T) {
	t.Run("Spawn with preset not in AvailablePresets returns error", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with a spawn call with invalid preset
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn",
					Name:             "spawn",
					Input:            `{"preset": "hallucinated_preset", "prompt": "do something"}`,
					AvailableTools:   []string{"spawn", "bash"},         // spawn is allowed
					AvailablePresets: []string{"researcher", "planner"}, // But preset is not
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "hallucinated_preset")
		assert.Contains(t, output.ToolResults[0].Content, "not available")

		// Spawn should NOT have been executed
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_spawn"))
	})

	t.Run("Spawn with valid preset executes normally", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with a valid spawn call
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn_valid",
					Name:             "spawn",
					Input:            `{"preset": "researcher", "prompt": "analyze code"}`,
					AvailableTools:   []string{"spawn", "bash"},
					AvailablePresets: []string{"researcher", "planner"},
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify - spawn should be executed (passes validation)
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		// Spawn execution may fail for other reasons (workflow not found),
		// but it should pass preset validation
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_spawn_valid"))
	})

	t.Run("Spawn with empty AvailablePresets skips preset validation", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with no AvailablePresets
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn_any",
					Name:             "spawn",
					Input:            `{"preset": "any_preset", "prompt": "do something"}`,
					AvailableTools:   []string{"spawn"},
					AvailablePresets: nil, // No preset validation
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify - spawn should be executed (no preset validation)
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_spawn_any"))
	})

	t.Run("Non-spawn tool ignores AvailablePresets", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with a non-spawn tool that has AvailablePresets
		// (should be ignored)
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_bash",
					Name:             "bash",
					Input:            `{"command": "ls"}`,
					AvailableTools:   []string{"bash", "spawn"},
					AvailablePresets: []string{"researcher"}, // Should be ignored for bash
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify - bash should execute normally
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_bash"))
	})
}

// TestExecuteToolsActivity_MixedValidation tests scenarios with both valid
// and invalid tool calls in the same batch.
func TestExecuteToolsActivity_MixedValidation(t *testing.T) {
	t.Run("Mixed valid and invalid tools", func(t *testing.T) {
		// Setup
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		// Create test data
		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Create mock executor
		mockExecutor := newMockToolExecutor()

		// Create activity
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// Create input with mix of valid and invalid tool calls
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:             "call_valid",
					Name:           "bash",
					Input:          `{"command": "ls"}`,
					AvailableTools: []string{"bash", "view"},
				},
				{
					ID:             "call_invalid",
					Name:           "fake_tool",
					Input:          `{}`,
					AvailableTools: []string{"bash", "view"},
				},
				{
					ID:             "call_valid2",
					Name:           "view",
					Input:          `{"file": "test.txt"}`,
					AvailableTools: []string{"bash", "view"},
				},
			},
		}

		// Execute
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		// Verify
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 3)

		// Check results by tool call ID
		resultMap := make(map[string]*reliantv1.ToolResultMsg)
		for _, r := range output.ToolResults {
			resultMap[r.GetToolCallId()] = r
		}

		// Valid tools should execute
		assert.False(t, resultMap["call_valid"].IsError)
		assert.False(t, resultMap["call_valid2"].IsError)

		// Invalid tool should fail validation
		assert.True(t, resultMap["call_invalid"].IsError)
		assert.Contains(t, resultMap["call_invalid"].Content, "not available")

		// Verify execution counts
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_valid"))
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_invalid"))
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_valid2"))
	})
}
