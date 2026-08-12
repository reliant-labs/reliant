// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The spawn path lives in Temporal WORKFLOW code, which must stay
// deterministic and therefore cannot call the repository. Its only route to
// durable state is this activity, so these tests are what proves spawn status
// survives a reload.

func setupEmitStatusFixture(t *testing.T) (*IdempotencyTestHelper, string) {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	return h, chatID
}

// A spawn moving executing -> completed leaves one row whose terminal status
// and child_workflow_id are both durable. The child workflow id is what makes
// a spawn's completion a join instead of string matching.
func TestEmitToolCallStatus_SpawnLifecycleIsDurable(t *testing.T) {
	h, chatID := setupEmitStatusFixture(t)
	defer h.Cleanup()
	ctx := context.Background()

	activityInstance := NewEmitToolCallStatusActivity(h.Repo())
	toolCallID := "toolu_" + uuid.New().String()
	childWorkflowID := "wf-" + uuid.New().String()

	var out EmitToolCallStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID:          chatID,
		ToolCallID:      toolCallID,
		ToolName:        "spawn",
		Status:          "executing",
		ChildWorkflowID: childWorkflowID,
		Input:           `{"prompt":"do the thing","preset":"researcher"}`,
	}, &out))
	assert.True(t, out.Success)

	call, err := h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err, "an executing spawn must be durable immediately")
	assert.Equal(t, core.ToolCallStatusExecuting, call.Status)
	require.NotNil(t, call.StartedAt)
	assert.Nil(t, call.CompletedAt)
	require.NotNil(t, call.ChildWorkflowID)
	assert.Equal(t, childWorkflowID, *call.ChildWorkflowID)
	assert.JSONEq(t, `{"prompt":"do the thing","preset":"researcher"}`, string(call.Input))

	// The spawn finishes.
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID:          chatID,
		ToolCallID:      toolCallID,
		ToolName:        "spawn",
		Status:          "completed",
		ChildWorkflowID: childWorkflowID,
		Input:           `{"prompt":"do the thing","preset":"researcher"}`,
	}, &out))

	call, err = h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, call.Status)
	require.NotNil(t, call.CompletedAt, "COMPLETED without completed_at violates the CHECK constraint")
	require.NotNil(t, call.StartedAt, "the executing transition's started_at must survive")

	// Still exactly one row for the whole lifecycle.
	calls, err := h.Repo().ListToolCallsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
}

func TestEmitToolCallStatus_SpawnFailureIsDurable(t *testing.T) {
	h, chatID := setupEmitStatusFixture(t)
	defer h.Cleanup()
	ctx := context.Background()

	activityInstance := NewEmitToolCallStatusActivity(h.Repo())
	toolCallID := "toolu_" + uuid.New().String()

	var out EmitToolCallStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID: chatID, ToolCallID: toolCallID, ToolName: "spawn", Status: "executing",
	}, &out))
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID: chatID, ToolCallID: toolCallID, ToolName: "spawn", Status: "failed",
	}, &out))

	call, err := h.Repo().GetToolCall(ctx, toolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)
	require.NotNil(t, call.CompletedAt)
}

// This activity is retried like any other, so the same status arriving twice
// must be a no-op on the row rather than a duplicate.
func TestEmitToolCallStatus_RetryIsIdempotent(t *testing.T) {
	h, chatID := setupEmitStatusFixture(t)
	defer h.Cleanup()
	ctx := context.Background()

	activityInstance := NewEmitToolCallStatusActivity(h.Repo())
	toolCallID := "toolu_" + uuid.New().String()

	input := EmitToolCallStatusInput{
		ChatID: chatID, ToolCallID: toolCallID, ToolName: "spawn", Status: "completed",
	}
	var out EmitToolCallStatusOutput
	for i := 0; i < 3; i++ {
		require.NoError(t, h.ExecuteActivity(activityInstance.Execute, input, &out))
	}

	calls, err := h.Repo().ListToolCallsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, calls, 1, "a retried activity must converge on one row")
	assert.Equal(t, core.ToolCallStatusCompleted, calls[0].Status)
}

// An unrecognized status string writes nothing rather than storing status 0,
// which no reader can interpret.
func TestEmitToolCallStatus_UnknownStatusWritesNothing(t *testing.T) {
	h, chatID := setupEmitStatusFixture(t)
	defer h.Cleanup()
	ctx := context.Background()

	activityInstance := NewEmitToolCallStatusActivity(h.Repo())
	toolCallID := "toolu_" + uuid.New().String()

	var out EmitToolCallStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID: chatID, ToolCallID: toolCallID, ToolName: "spawn", Status: "who-knows",
	}, &out))

	calls, err := h.Repo().ListToolCallsByChat(ctx, chatID)
	require.NoError(t, err)
	assert.Empty(t, calls, "an uninterpretable status must not create a row")
}

// The event emission and the durable write are complements, not alternatives:
// persistence must not disturb the chat_updates stream the live UI consumes.
func TestEmitToolCallStatus_StillEmitsChatUpdate(t *testing.T) {
	h, chatID := setupEmitStatusFixture(t)
	defer h.Cleanup()
	ctx := context.Background()

	activityInstance := NewEmitToolCallStatusActivity(h.Repo())
	toolCallID := "toolu_" + uuid.New().String()

	var out EmitToolCallStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, EmitToolCallStatusInput{
		ChatID: chatID, ToolCallID: toolCallID, ToolName: "spawn", Status: "executing",
	}, &out))
	assert.True(t, out.Success)

	var updateCount int
	require.NoError(t, h.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM chat_updates WHERE chat_id = $1`, chatID).Scan(&updateCount))
	assert.Positive(t, updateCount, "the transient event stream must still be emitted")
}

func TestToolCallStatusFromString(t *testing.T) {
	cases := map[string]core.ToolCallStatus{
		"pending":      core.ToolCallStatusPending,
		"executing":    core.ToolCallStatusExecuting,
		"completed":    core.ToolCallStatusCompleted,
		"failed":       core.ToolCallStatusFailed,
		"cancelled":    core.ToolCallStatusCancelled,
		"backgrounded": core.ToolCallStatusBackgrounded,
		"":             core.ToolCallStatusUnspecified,
		"nonsense":     core.ToolCallStatusUnspecified,
	}
	for input, want := range cases {
		assert.Equal(t, want, toolCallStatusFromString(input), "status %q", input)
	}
}
