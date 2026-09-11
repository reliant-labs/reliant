// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TOOL PERMISSION ENFORCEMENT TESTS
// ============================================================================

// TestExecuteToolsActivity_PermissionEnforcement tests that tool calls are
// validated at execution against BOTH the declared tool set and the permission
// level set by call_llm via LoadedToolsStore.
func TestExecuteToolsActivity_PermissionEnforcement(t *testing.T) {
	t.Run("Tool outside the declared set is denied", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Plan mode is expressed as a DECLARED TOOL SET, not a permission tier.
		// The readonly tier used to carry this and never actually prevented a
		// write — the shell was granted at that tier too. The filter does, and
		// is enforced here at execution.
		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionMutating)
		tools.GetLoadedToolsStore().SetAllowedTools(tools.Scope(chatID, "0"),
			[]string{tools.ToolView, tools.ShellToolName})
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_write",
					Name:  tools.ToolWrite,
					Input: `{"file_path": "/tmp/x", "content": "y"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "declared tool set")

		// Tool should NOT have been executed
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_write"))
	})

	t.Run("Tool inside the declared set runs", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// A tool inside the declared set runs normally, so the guard above is
		// not just refusing everything.
		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionMutating)
		tools.GetLoadedToolsStore().SetAllowedTools(tools.Scope(chatID, "0"),
			[]string{tools.ToolView, tools.ShellToolName})
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		// view is inside the declared set
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

		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionMutating)
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

	// An unset scope is the worker-restart case: the in-memory store was emptied
	// while the run was in flight. It must fail CLOSED — falling back to the
	// lowest live tier rather than granting orchestrator precisely because the
	// grant was lost. An undeclared scope still allows ordinary tools, so a live
	// run is not stranded.
	t.Run("Unset permission falls back to the base tier and still runs ordinary tools", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Deliberately set no permission for this scope.
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

	// The other half of the same case, and the one the old orchestrator default
	// got wrong: losing the grant must not widen it.
	//
	// spawn is what this can be shown with now. With the readonly tier removed,
	// the base tier IS mutating, so there is no longer a "mutating tool" an
	// ungranted scope can be denied — the fail-closed default withholds exactly
	// one capability, and that is the one worth pinning.
	t.Run("Unset permission denies spawn", func(t *testing.T) {
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()

		ctx := context.Background()

		userID := uuid.New().String()
		projectID := uuid.New().String()
		chatID := uuid.New().String()

		h.CreateTestProject(ctx, projectID, userID)
		h.CreateTestChat(ctx, chatID, projectID, userID)

		// Deliberately set no permission for this scope.
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_spawn",
					Name:  "spawn",
					Input: `{"preset": "general", "prompt": "x"}`,
				},
			},
		}

		var output ExecuteToolsOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)

		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError,
			"spawn must be denied when the scope carries no granted permission")
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_spawn"),
			"a denied tool must never reach the executor")
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
		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionOrchestrator)
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

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

		// A declared set of view + bash: both run, write is refused. bash is in
		// the set because with the scoped search tools gone the shell is the
		// only way to search, so a planning agent needs it. It can still
		// redirect into a file — which is exactly why this is authorial intent
		// and not a security boundary; a hard boundary lives below the tool
		// layer.
		tools.GetLoadedToolsStore().SetPermission(tools.Scope(chatID, "0"), tools.PermissionMutating)
		tools.GetLoadedToolsStore().SetAllowedTools(tools.Scope(chatID, "0"),
			[]string{tools.ToolView, "bash"})
		defer tools.GetLoadedToolsStore().Clear(tools.Scope(chatID, "0"))

		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		input := ExecuteToolsInput{
			ChatID: chatID,
			Thread: "0",
			ToolCalls: []ToolCall{
				{
					ID:    "call_view",
					Name:  tools.ToolView,
					Input: `{"file_path": "test.txt"}`,
				},
				{
					ID:    "call_write",
					Name:  tools.ToolWrite,
					Input: `{"file_path": "/tmp/x", "content": "y"}`,
				},
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
		require.Len(t, output.ToolResults, 3)

		resultMap := make(map[string]*reliantv1.ToolResultMsg)
		for _, r := range output.ToolResults {
			resultMap[r.GetToolCallId()] = r
		}

		// Readonly-tier tools should execute
		assert.False(t, resultMap["call_view"].IsError)
		assert.False(t, resultMap["call_bash"].IsError)

		// Mutating tool should be denied
		assert.True(t, resultMap["call_write"].IsError)
		assert.Contains(t, resultMap["call_write"].Content, "declared tool set")

		// Verify execution counts
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_view"))
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_write"))
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_bash"))
	})
}
