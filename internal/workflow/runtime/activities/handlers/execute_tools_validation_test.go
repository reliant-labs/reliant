// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// EXECUTION-TIME CAPABILITY ENFORCEMENT
// ============================================================================
//
// execute_tools enforces the capability set of the call_llm turn that produced
// its calls, carried on ExecuteToolsArgs.capabilities. These pin that check in
// isolation; capabilities_flow_test.go drives it from a real call_llm turn.

// offeredCaps is a recorded set whose turn offered exactly names, at tier.
func offeredCaps(permission string, names ...string) *reliantv1.ToolCapabilities {
	return (&tools.Capabilities{Offered: names, Permission: permission, LoadableAll: true}).Proto()
}

// newValidationChat creates a chat to execute against.
func newValidationChat(t *testing.T) (*IdempotencyTestHelper, string) {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	return h, chatID
}

func TestExecuteToolsActivity_CapabilityEnforcement(t *testing.T) {
	// A tool outside the preloaded bundle that load_tool granted on an earlier
	// turn is OFFERED on this one, so it runs. This is the case the old
	// declared-set check got wrong — it refused generate_image after
	// load_tool had legitimately granted it — and the reason that check was
	// removed rather than fixed. "Offered in the request that produced this
	// call" includes loaded tools by construction.
	t.Run("A loaded tool outside the preloaded bundle executes", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:       chatID,
			Thread:       "0",
			Capabilities: offeredCaps(tools.PermissionMutating, tools.ToolView, tools.ShellToolName, tools.ToolGenerateImage),
			ToolCalls: []message.ToolCall{
				{ID: "call_image", Name: tools.ToolGenerateImage, Input: `{"prompt": "a cat"}`},
			},
		}, &output))

		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError, output.ToolResults[0].Content)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_image"))
	})

	t.Run("A tool that was not offered is refused and never executed", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:       chatID,
			Thread:       "0",
			Capabilities: offeredCaps(tools.PermissionMutating, tools.ToolView),
			ToolCalls: []message.ToolCall{
				{ID: "call_view", Name: tools.ToolView, Input: `{"file_path": "test.txt"}`},
				{ID: "call_write", Name: tools.ToolWrite, Input: `{"file_path": "/tmp/x", "content": "y"}`},
			},
		}, &output))

		results := resultsByID(output.ToolResults)
		assert.False(t, results["call_view"].IsError)
		assert.True(t, results["call_write"].IsError)
		assert.Contains(t, results["call_write"].Content, "Tool 'write' was not offered on this turn")
		assert.Contains(t, results["call_write"].Content, `load_tool(name="write")`,
			"a loadable tool's refusal says how to get it")
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_view"))
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_write"), "a refused tool must never reach the executor")
	})

	// The tier is part of the set: a tool above it is never offered, so a call
	// to one is refused by the same check, with the tier as the reason.
	t.Run("A tool above the tier is refused mid-batch", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		caps := tools.ResolveCapabilities(tools.CapabilityInputs{
			Access:     tools.ResolveToolAccess([]string{tools.ToolView, tools.ToolStartRun}, nil, nil),
			Permission: tools.PermissionMutating,
		})
		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:       chatID,
			Thread:       "0",
			Capabilities: caps.Proto(),
			ToolCalls: []message.ToolCall{
				{ID: "call_view", Name: tools.ToolView, Input: `{"file_path": "test.txt"}`},
				{ID: "call_start_run", Name: tools.ToolStartRun, Input: `{"prompt": "x"}`},
			},
		}, &output))

		results := resultsByID(output.ToolResults)
		assert.False(t, results["call_view"].IsError)
		assert.True(t, results["call_start_run"].IsError)
		assert.Contains(t, results["call_start_run"].Content, "requires 'orchestrator' permission")
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_start_run"))
	})

	// No recorded set: a batch whose call_llm predates the set. It keeps what
	// a worker restart already produced — no offered check, the base tier.
	t.Run("With no recorded set an ordinary tool runs", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:    chatID,
			Thread:    "0",
			ToolCalls: []message.ToolCall{{ID: "call_bash", Name: "bash", Input: `{"command": "ls"}`}},
		}, &output))

		require.Len(t, output.ToolResults, 1)
		assert.False(t, output.ToolResults[0].IsError)
		assert.Equal(t, 1, mockExecutor.GetExecutionCount("call_bash"))
	})

	t.Run("With no recorded set an orchestrator tool is refused at the base tier", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:    chatID,
			Thread:    "0",
			ToolCalls: []message.ToolCall{{ID: "call_start_run", Name: tools.ToolStartRun, Input: `{"prompt": "x"}`}},
		}, &output))

		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "requires 'orchestrator' permission")
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_start_run"))
	})
}

// A refusal is recorded FAILED with its reason, like every other refusal, so a
// reload shows a refused call rather than one stuck "executing".
func TestExecuteTools_RefusesToolNotOfferedThisTurn(t *testing.T) {
	h, chatID := newValidationChat(t)
	mockExecutor := newMockToolExecutor()
	activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)
	toolCallID := "toolu_" + uuid.NewString()

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID:       chatID,
		Thread:       chatID,
		Capabilities: (&tools.Capabilities{Offered: []string{tools.ToolCreatePlan, tools.ToolView}, Permission: tools.PermissionMutating}).Proto(),
		// write was offered on an earlier, agent-mode turn and is still in
		// history; this turn (plan mode) did not offer it.
		ToolCalls: []message.ToolCall{{ID: toolCallID, Name: tools.ToolWrite, Input: `{"file_path": "/tmp/x", "content": "y"}`}},
	}, &output))

	require.Len(t, output.ToolResults, 1)
	result := output.ToolResults[0]
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "was not offered to this agent")
	assert.Equal(t, 0, mockExecutor.GetExecutionCount(toolCallID))

	call, err := h.Repo().GetToolCall(context.Background(), toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)
	require.NotNil(t, call.ErrorMessage)
	assert.Equal(t, result.Content, *call.ErrorMessage, "the row records why")
	stored := getToolCallResult(t, h, toolCallID)
	require.NotNil(t, stored, "a refused call still owes the conversation a result row")
	assert.True(t, stored.IsError)
}

// A spawn reaches the activity only when the workflow declined to dispatch it
// (runtime.withCapabilitiesApplied). It is never executed here.
func TestExecuteToolsActivity_SpawnRoutedHereIsRefused(t *testing.T) {
	spawnCaps := (&tools.Capabilities{
		Offered:      []string{tools.ToolSpawn},
		Permission:   tools.PermissionMutating,
		SpawnPresets: []string{"planner", "researcher"},
	}).Proto()

	t.Run("A preset the spawn tool was not offered with names the offered ones", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:       chatID,
			Thread:       "0",
			Capabilities: spawnCaps,
			ToolCalls: []message.ToolCall{
				{ID: "call_spawn", Name: tools.ToolSpawn, Input: `{"preset": "hallucinated_preset", "prompt": "do something"}`},
			},
		}, &output))

		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "Preset 'hallucinated_preset' is not available")
		assert.Contains(t, output.ToolResults[0].Content, "planner")
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_spawn"))
	})

	t.Run("A spawn that was not offered says so", func(t *testing.T) {
		h, chatID := newValidationChat(t)
		mockExecutor := newMockToolExecutor()
		activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)

		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID:       chatID,
			Thread:       "0",
			Capabilities: offeredCaps(tools.PermissionMutating, tools.ToolView),
			ToolCalls: []message.ToolCall{
				{ID: "call_spawn", Name: tools.ToolSpawn, Input: `{"preset": "planner", "prompt": "x"}`},
			},
		}, &output))

		require.Len(t, output.ToolResults, 1)
		assert.True(t, output.ToolResults[0].IsError)
		assert.Contains(t, output.ToolResults[0].Content, "Spawning sub-agents is not available")
		assert.Equal(t, 0, mockExecutor.GetExecutionCount("call_spawn"))
	})
}

func resultsByID(results []*reliantv1.ToolResultMsg) map[string]*reliantv1.ToolResultMsg {
	byID := make(map[string]*reliantv1.ToolResultMsg, len(results))
	for _, r := range results {
		byID[r.GetToolCallId()] = r
	}
	return byID
}
