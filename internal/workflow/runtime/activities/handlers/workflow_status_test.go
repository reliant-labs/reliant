// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TESTS FOR WORKFLOWSTATUSACTIVITY
// ============================================================================
//
// Test Coverage:
// 1. Root workflow updates existing thread's workflow_id
// 2. Child workflow updates existing workflow status (parent creates workflow+thread)
// 3. Child workflow that doesn't exist returns error
// 4. Child workflow already running is a no-op
// 5. Completed/failed/cancelled/yielded status updates
//
// ============================================================================

func TestWorkflowStatus_RootWorkflowUpdatesThread(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	// CreateTestChat creates threads with ID=chatID and ID="0"
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Verify the thread exists but has NULL workflow_id (simulating ChatService creation)
	thread, err := h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	assert.Nil(t, thread.WorkflowID, "Thread should have NULL workflow_id initially")

	// Create activity
	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   workflowID,
		WorkflowName: "builtin://chat",
		Status:       "started",
		Thread:       chatID, // Root workflow uses chatID as thread
		// ParentWorkflowID is empty for root workflows
	}

	t.Run("Root workflow updates existing thread workflow_id", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Verify thread now has the workflow_id
		updatedThread, err := h.Repo().GetThread(ctx, chatID)
		require.NoError(t, err)
		require.NotNil(t, updatedThread.WorkflowID, "Thread should have workflow_id after root workflow started")
		assert.Equal(t, workflowID, *updatedThread.WorkflowID, "Thread workflow_id should match the workflow")
	})
}

func TestWorkflowStatus_ChildWorkflowUpdatesExistingWorkflow(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	parentWorkflowID := uuid.New().String()
	childWorkflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create parent workflow (simulating root workflow already started)
	parentWorkflow := &db.Workflow{
		ID:           parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, parentWorkflow)
	require.NoError(t, err)

	// Pre-create child workflow with pending status (simulating parent's initChildWorkflow)
	childWorkflow := &db.Workflow{
		ID:           childWorkflowID,
		ParentID:     &parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://sub-agent",
		Thread:       childWorkflowID,
		Status:       db.WorkflowStatusPending,
	}
	err = h.Repo().CreateWorkflow(ctx, childWorkflow)
	require.NoError(t, err)

	// Pre-create child thread (simulating parent's V2_CreateWorkflowWithThread)
	_, err = h.Repo().CreateThread(ctx, &db.Thread{
		ID:             childWorkflowID,
		ConversationID: chatID,
		Title:          ptr.Of("Sub-Agent Task"),
		WorkflowID:     &childWorkflowID,
	})
	require.NoError(t, err)

	// Create activity
	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:           chatID,
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://sub-agent",
		Status:           "started",
		Thread:           childWorkflowID,
		ParentWorkflowID: parentWorkflowID,
		ThreadTitle:      "Sub-Agent Task",
	}

	t.Run("Child workflow updates existing workflow status", func(t *testing.T) {
		var output WorkflowStatusOutput
		err = h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Verify workflow status was updated to running
		updatedWorkflow, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusRunning, updatedWorkflow.Status, "Workflow should be running after started")
	})
}

func TestWorkflowStatus_ChildWorkflowMustExist(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	parentWorkflowID := uuid.New().String()
	childWorkflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create parent workflow (simulating root workflow already started)
	parentWorkflow := &db.Workflow{
		ID:           parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, parentWorkflow)
	require.NoError(t, err)

	// Create activity - DO NOT pre-create the child workflow
	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:           chatID,
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://sub-agent",
		Status:           "started",
		Thread:           childWorkflowID,
		ParentWorkflowID: parentWorkflowID,
	}

	t.Run("Child workflow that doesn't exist returns error", func(t *testing.T) {
		var output WorkflowStatusOutput
		err = h.ExecuteActivity(activity.Execute, input, &output)
		// The activity itself succeeds (returns Success=true) but trackWorkflow logs a warning
		// The error is logged but doesn't fail the activity since chat_update is more important
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Verify workflow was NOT created (since parent didn't create it)
		_, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
		assert.Error(t, err, "Workflow should not exist - parent must create it first")
	})
}

func TestWorkflowStatus_CompletedUpdatesStatus(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create a running workflow
	workflow := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, workflow)
	require.NoError(t, err)

	// Create activity
	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   workflowID,
		WorkflowName: "builtin://chat",
		Status:       "completed",
		Thread:       chatID,
	}

	t.Run("Completed status updates workflow", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Verify workflow status was updated
		updatedWorkflow, err := h.Repo().GetWorkflow(ctx, workflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusCompleted, updatedWorkflow.Status)
	})
}

func TestWorkflowStatus_YieldedUpdatesStatusAndCompletesOwnedThreads(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	parentWorkflowID := uuid.New().String()
	childWorkflowID := uuid.New().String()
	threadRecordWorkflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create a running parent workflow
	parentWorkflow := &db.Workflow{
		ID:           parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, parentWorkflow)
	require.NoError(t, err)

	// Create a running child workflow (the one that will be force-yielded)
	childWorkflow := &db.Workflow{
		ID:           childWorkflowID,
		ParentID:     &parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://sub-agent",
		Thread:       childWorkflowID,
		Status:       db.WorkflowStatusRunning,
	}
	err = h.Repo().CreateWorkflow(ctx, childWorkflow)
	require.NoError(t, err)

	// Create a running thread metadata record owned by the child workflow.
	// This should be completed by CompleteChildThreadRecords when yielded is processed.
	threadRecord := &db.Workflow{
		ID:           threadRecordWorkflowID,
		ParentID:     &childWorkflowID,
		ChatID:       chatID,
		WorkflowName: "thread:child-thread",
		Thread:       "child-thread",
		Status:       db.WorkflowStatusRunning,
	}
	err = h.Repo().CreateWorkflow(ctx, threadRecord)
	require.NoError(t, err)

	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:           chatID,
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://sub-agent",
		Status:           "yielded",
		Thread:           childWorkflowID,
		ParentWorkflowID: parentWorkflowID,
	}

	t.Run("Yielded status updates workflow and completes owned thread records", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Yielded workflow should be persisted as completed (terminal).
		updatedChildWorkflow, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusCompleted, updatedChildWorkflow.Status)

		// Owned thread metadata workflow should also be completed.
		updatedThreadRecord, err := h.Repo().GetWorkflow(ctx, threadRecordWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusCompleted, updatedThreadRecord.Status)
	})
}

// TestWorkflowStatus_ChildWorkflowAlreadyRunningNoOp tests that when a child workflow
// is already running, WorkflowStatus doesn't try to update it again.
func TestWorkflowStatus_ChildWorkflowAlreadyRunningNoOp(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	parentWorkflowID := uuid.New().String()
	childWorkflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create parent workflow
	parentWorkflow := &db.Workflow{
		ID:           parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, parentWorkflow)
	require.NoError(t, err)

	// Pre-create child workflow with RUNNING status (already started)
	childWorkflow := &db.Workflow{
		ID:           childWorkflowID,
		ParentID:     &parentWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://sub-agent",
		Thread:       childWorkflowID,
		Status:       db.WorkflowStatusRunning, // Already running
	}
	err = h.Repo().CreateWorkflow(ctx, childWorkflow)
	require.NoError(t, err)

	// Pre-create child thread
	_, err = h.Repo().CreateThread(ctx, &db.Thread{
		ID:             childWorkflowID,
		ConversationID: chatID,
		WorkflowID:     &childWorkflowID,
	})
	require.NoError(t, err)

	// Create activity
	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:           chatID,
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://sub-agent",
		Status:           "started",
		Thread:           childWorkflowID,
		ParentWorkflowID: parentWorkflowID,
	}

	t.Run("Child workflow already running is a no-op", func(t *testing.T) {
		var output WorkflowStatusOutput
		err = h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// Verify workflow is still running (no change)
		updatedWorkflow, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusRunning, updatedWorkflow.Status, "Workflow should still be running")
	})
}

// TestWorkflowStatus_PausedUpdatesStatus verifies that the "paused" status
// correctly maps to db.WorkflowStatusPaused. This is the new status used by
// the rate-limit auto-pause feature: when a retryable error exhausts Temporal's
// retry budget, the workflow self-pauses and emits status="paused" so the UI
// can reflect the paused state and SendMessage routes through resume.
func TestWorkflowStatus_PausedUpdatesStatus(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create a running workflow (paused comes from a running workflow)
	wf := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, wf)
	require.NoError(t, err)

	activity := NewWorkflowStatusActivity(h.Repo())

	input := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   workflowID,
		WorkflowName: "builtin://chat",
		Status:       "paused",
		Thread:       chatID,
	}

	t.Run("Paused status updates workflow to paused", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		updatedWorkflow, err := h.Repo().GetWorkflow(ctx, workflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusPaused, updatedWorkflow.Status,
			"Workflow should be paused after 'paused' status update")
	})
}

// TestWorkflowStatus_PausedThenRestarted verifies the full pause→resume lifecycle:
// workflow goes from running→paused→running.
func TestWorkflowStatus_PausedThenRestarted(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	wf := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://chat",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}
	err := h.Repo().CreateWorkflow(ctx, wf)
	require.NoError(t, err)

	activity := NewWorkflowStatusActivity(h.Repo())

	t.Run("Pause then restart lifecycle", func(t *testing.T) {
		// Pause
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:       chatID,
			WorkflowID:   workflowID,
			WorkflowName: "builtin://chat",
			Status:       "paused",
			Thread:       chatID,
		}, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		pausedWf, err := h.Repo().GetWorkflow(ctx, workflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusPaused, pausedWf.Status)

		// Resume (re-emit "started")
		err = h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:       chatID,
			WorkflowID:   workflowID,
			WorkflowName: "builtin://chat",
			Status:       "started",
			Thread:       chatID,
		}, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		resumedWf, err := h.Repo().GetWorkflow(ctx, workflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusRunning, resumedWf.Status,
			"Workflow should be running after re-emitting 'started' status")
	})
}
