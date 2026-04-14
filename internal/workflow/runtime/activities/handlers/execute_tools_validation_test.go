// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TOOL PERMISSION ENFORCEMENT TESTS
// ============================================================================

// TestExecuteToolsActivity_PermissionEnforcement tests that tool calls are validated
// against the permission level set by call_llm via LoadedToolsStore.
func TestExecuteToolsActivity_PermissionEnforcement(t *testing.T) {
	t.Run("Mutating tool denied with readonly permission", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Set readonly permission (simulates plan mode)
		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionReadOnly)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// bash requires mutating permission
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_bash",
					Name:  "bash",
					Input: `{"command": "ls"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "permission")
		assert.Contains(t, output.ToolResults[0].Content, "readonly")

		// Tool should NOT have been executed
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_bash"))
	})

	t.Run("Read-only tool allowed with readonly permission", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Set readonly permission
		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionReadOnly)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// view is a read-only tool
		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_view",
					Name:  "view",
					Input: `{"file_path": "test.txt"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_view"))
	})

	t.Run("Mutating tool allowed with mutating permission", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionMutating)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_bash",
					Name:  "bash",
					Input: `{"command": "ls"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_bash"))
	})

	t.Run("Default permission is orchestrator (allows everything)", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Don't set permission — should default to orchestrator
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_any",
					Name:  "bash",
					Input: `{"command": "ls"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_any"))
	})
}

// TestExecuteToolsActivity_SpawnPresetValidation tests that spawn tool calls
// are validated against the AvailablePresets list.
func TestExecuteToolsActivity_SpawnPresetValidation(t *testing.T) {
	t.Run("Spawn with preset not in AvailablePresets returns error", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Orchestrator permission so spawn itself is allowed
		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn",
					Name:             "spawn",
					Input:            `{"preset": "hallucinated_preset", "prompt": "do something"}`,
					AvailablePresets: []string{"researcher", "planner"},
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "hallucinated_preset")
		assert.Contains(t, output.ToolResults[0].Content, "not available")

		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_spawn"))
	})

	t.Run("Spawn with valid preset executes normally", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn_valid",
					Name:             "spawn",
					Input:            `{"preset": "researcher", "prompt": "analyze code"}`,
					AvailablePresets: []string{"researcher", "planner"},
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_spawn_valid"))
	})

	t.Run("Spawn with empty AvailablePresets skips preset validation", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_spawn_any",
					Name:             "spawn",
					Input:            `{"preset": "any_preset", "prompt": "do something"}`,
					AvailablePresets: nil, // No preset validation
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_spawn_any"))
	})

	t.Run("Non-spawn tool ignores AvailablePresets", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:               "call_bash",
					Name:             "bash",
					Input:            `{"command": "ls"}`,
					AvailablePresets: []string{"researcher"}, // Should be ignored for bash
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_bash"))
	})
}

// TestExecuteToolsActivity_MixedPermissions tests scenarios with tools
// requiring different permission levels in the same batch.
func TestExecuteToolsActivity_MixedPermissions(t *testing.T) {
	t.Run("Mixed allowed and denied tools", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Set readonly — view is allowed, bash is denied
		tools.GetLoadedToolsStore().SetPermission(chatID, tools.PermissionReadOnly)
		defer tools.GetLoadedToolsStore().Clear(chatID)

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_view",
					Name:  "view",
					Input: `{"file_path": "test.txt"}`,
				},
				{
					ID:    "call_bash",
					Name:  "bash",
					Input: `{"command": "rm -rf /"}`,
				},
				{
					ID:    "call_grep",
					Name:  "grep",
					Input: `{"pattern": "foo"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 3)

		resultMap := make(map[string]*reliantv1.ToolResultMsg)
		for _, r := range output.ToolResults {
			resultMap[r.GetToolCallId()] = r
		}

		// Read-only tools should execute
		assert.False(t, resultMap["call_view"].IsError)
		assert.False(t, resultMap["call_grep"].IsError)

		// Mutating tool should be denied
		assert.True(t, resultMap["call_bash"].IsError)
		assert.Contains(t, resultMap["call_bash"].Content, "permission")

		// Verify execution counts
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_view"))
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_bash"))
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_grep"))
	})
}
