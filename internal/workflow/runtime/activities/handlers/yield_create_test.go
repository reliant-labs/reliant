// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestYieldCreateActivity_MarksChatUnread verifies that when a new yield is created,
// the chat is marked as unread so the UI shows a notification badge.
func TestYieldCreateActivity_MarksChatUnread(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	activity := NewYieldCreateActivity(h.Repo())

	input := YieldCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         "agent_loop",
		LoopIteration:      0,
	}

	var output YieldCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.NotEmpty(t, output.YieldID)
	assert.False(t, output.AlreadyResolved)

	// Verify chat was marked as unread
	chat, err := h.Repo().GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, chat.Unread,
		"Chat should be marked as unread after yield creation")
}

// TestYieldCreateActivity_EmitsYieldUpdate verifies that creating a yield emits a yield chat_update
// record in the same transaction, so the frontend receives the update via the streaming API.
func TestYieldCreateActivity_EmitsYieldUpdate(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	activity := NewYieldCreateActivity(h.Repo())

	input := YieldCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         "agent_loop",
		LoopIteration:      0,
	}

	var output YieldCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.NotEmpty(t, output.YieldID)

	// Query chat_updates table to verify the yield update was emitted
	var count int
	err = h.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_updates WHERE chat_id = ? AND update_type = ?`,
		chatID, int32(db.UpdateTypeYield),
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "Expected exactly 1 yield chat_update to be emitted")
}

// TestYieldCreateActivity_Idempotency verifies that calling YieldCreate twice for the same
// workflow+step+iteration returns the existing yield rather than creating a duplicate.
func TestYieldCreateActivity_Idempotency(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	// Pre-create a pending yield for the same workflow+step+iteration
	existingYieldID := uuid.New().String()
	loopNodeID := "agent_loop"
	loopIteration := 0
	err := h.Repo().CreateYield(ctx, &db.Yield{
		ID:                 existingYieldID,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)

	activity := NewYieldCreateActivity(h.Repo())

	input := YieldCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         "agent_loop",
		LoopIteration:      0,
	}

	var output YieldCreateOutput
	err = h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// Should return the existing yield, not create a new one
	assert.Equal(t, existingYieldID, output.YieldID)
	assert.False(t, output.AlreadyResolved)

	// Verify no duplicate was created
	yields, err := h.Repo().GetYieldsByWorkflowStepIteration(ctx, workflowID, "agent_loop", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, len(yields), "Should not create a duplicate yield")
}

// TestYieldCreateActivity_IdempotencyAlreadyResolved verifies that if the existing yield is
// already resolved, YieldCreate returns AlreadyResolved=true with the action taken.
func TestYieldCreateActivity_IdempotencyAlreadyResolved(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	// Pre-create an already-resolved yield
	existingYieldID := uuid.New().String()
	loopNodeID := "agent_loop"
	loopIteration := 0
	err := h.Repo().CreateYield(ctx, &db.Yield{
		ID:                 existingYieldID,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)
	err = h.Repo().ResolveYield(ctx, existingYieldID, "reply")
	require.NoError(t, err)

	activity := NewYieldCreateActivity(h.Repo())

	input := YieldCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         "agent_loop",
		LoopIteration:      0,
	}

	var output YieldCreateOutput
	err = h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	assert.Equal(t, existingYieldID, output.YieldID)
	assert.True(t, output.AlreadyResolved, "Should report yield as already resolved")
	assert.Equal(t, "reply", output.ActionTaken)
}

// TestYieldCreateActivity_SetsTemporalWorkflowIDFallback verifies that when TemporalWorkflowID
// is empty, the yield record falls back to using WorkflowID as the temporal workflow ID.
func TestYieldCreateActivity_SetsTemporalWorkflowIDFallback(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	activity := NewYieldCreateActivity(h.Repo())

	// Deliberately leave TemporalWorkflowID empty
	input := YieldCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: "",
		ThreadID:           chatID,
		StepID:             "agent_loop",
		LoopNodeID:         "agent_loop",
		LoopIteration:      0,
	}

	var output YieldCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// Verify the yield record used WorkflowID as the temporal workflow ID fallback
	yield, err := h.Repo().GetYieldByID(ctx, output.YieldID)
	require.NoError(t, err)
	require.NotNil(t, yield)
	assert.Equal(t, workflowID, yield.TemporalWorkflowID,
		"TemporalWorkflowID should fall back to WorkflowID when not provided")
}
