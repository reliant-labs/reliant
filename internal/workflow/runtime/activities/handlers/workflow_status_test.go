// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

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
// 5. Completed/failed/cancelled status updates
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
		ID:         childWorkflowID,
		ChatID:     chatID,
		Title:      ptr.Of("Sub-Agent Task"),
		WorkflowID: &childWorkflowID,
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

func TestWorkflowStatus_ChildWorkflowCreatedOnMissing(t *testing.T) {
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

	t.Run("Child workflow that doesn't exist is created on missing", func(t *testing.T) {
		var output WorkflowStatusOutput
		err = h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.True(t, output.Success)

		// The status write can race ahead of the parent's
		// CreateWorkflowWithThread commit; after a short retry the activity
		// creates the row itself (CreateWorkflow is idempotent, so the
		// parent's later create is a no-op).
		created, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
		require.NoError(t, err, "child workflow row should be created on missing")
		require.NotNil(t, created)
		assert.Equal(t, db.WorkflowStatusRunning, created.Status)
		require.NotNil(t, created.ParentID)
		assert.Equal(t, parentWorkflowID, *created.ParentID)
		assert.Equal(t, childWorkflowID, created.Thread)
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
		ID:         childWorkflowID,
		ChatID:     chatID,
		WorkflowID: &childWorkflowID,
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

func TestWorkflowStatus_NestedSelfPausePropagatesChatWide(t *testing.T) {
	// A self-pause (retry exhaustion, daemon-offline breaker) parks the ENTIRE
	// Temporal execution, but the notifying executor may be a nested spawn
	// whose workflow_id is not the root row. The "paused" write must land
	// chat-wide — the root row is what SendMessage's resume routing and the
	// reconciler's progress-watchdog pause exclusion consult. The "started"
	// notify with Resumed=true is the mirror: it must un-pause chat-wide.
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	rootWorkflowID := uuid.New().String()
	spawnWorkflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}))
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID:           spawnWorkflowID,
		ParentID:     &rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       spawnWorkflowID,
		Status:       db.WorkflowStatusRunning,
	}))

	activity := NewWorkflowStatusActivity(h.Repo())

	t.Run("nested paused marks the root row paused too", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:       chatID,
			WorkflowID:   spawnWorkflowID,
			WorkflowName: "builtin://agent",
			Status:       "paused",
		}, &output)
		require.NoError(t, err)

		spawn, err := h.Repo().GetWorkflow(ctx, spawnWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusPaused, spawn.Status)

		root, err := h.Repo().GetWorkflow(ctx, rootWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusPaused, root.Status,
			"self-pause must mark the ROOT row paused: SendMessage resume routing and the stall watchdog key off it")
	})

	t.Run("nested resumed started un-pauses chat-wide", func(t *testing.T) {
		var output WorkflowStatusOutput
		err := h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:       chatID,
			WorkflowID:   spawnWorkflowID,
			WorkflowName: "builtin://agent",
			Status:       "started",
			Resumed:      true,
		}, &output)
		require.NoError(t, err)

		spawn, err := h.Repo().GetWorkflow(ctx, spawnWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusRunning, spawn.Status)

		root, err := h.Repo().GetWorkflow(ctx, rootWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusRunning, root.Status,
			"post-resume started must un-park the whole chat, not just the notifying row")
	})

	t.Run("plain spawn started does not un-pause a paused chat", func(t *testing.T) {
		// Re-pause, then send a NON-resumed "started" for the spawn: only the
		// spawn's own row may change. A stale spawn-start notify must never
		// undo a chat-wide pause.
		var output WorkflowStatusOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:       chatID,
			WorkflowID:   spawnWorkflowID,
			WorkflowName: "builtin://agent",
			Status:       "paused",
		}, &output))

		require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
			ChatID:           chatID,
			WorkflowID:       spawnWorkflowID,
			WorkflowName:     "builtin://agent",
			Status:           "started",
			ParentWorkflowID: rootWorkflowID,
		}, &output))

		root, err := h.Repo().GetWorkflow(ctx, rootWorkflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusPaused, root.Status,
			"a non-resumed started must not un-pause the root row")
	})
}

// TestWorkflowStatus_StartedRevivesTerminalThread pins the missing half of
// the thread lifecycle at the site that writes it.
//
// threads.status was only ever written in the CLOSING direction. Harmless for
// a spawned sub-agent (fresh thread per run, ends once), but a chat's MAIN
// thread is reused for every turn: SendMessage starts a new Temporal run
// under the same workflow ID and the same thread ID. Turn N stamped the
// thread terminal, and this activity's "started" arm revived only the
// WORKFLOW row -- so from turn 2 onward every chat ran with a live workflow
// behind a thread that still read completed.
//
// That is what made SendAgentMessage refuse to queue into a working agent
// with "This agent has already finished (status: completed)". Measured on the
// live DB at the time of the fix: 3 chats in exactly this state.
func TestWorkflowStatus_StartedRevivesTerminalThread(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewWorkflowStatusActivity(h.Repo())
	startedInput := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   chatID,
		WorkflowName: "builtin://chat",
		Status:       "started",
		Thread:       chatID,
	}

	// Turn 1 runs and completes, stamping both halves terminal.
	var output WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, startedInput, &output))
	completedAt := time.Now().UTC()
	_, err := h.Repo().UpdateThreadStatus(ctx, chatID, db.ThreadStatusCompleted, &completedAt)
	require.NoError(t, err)
	require.NoError(t, h.Repo().UpdateWorkflowStatus(ctx, chatID, db.WorkflowStatusCompleted))

	// Turn 2 starts on the SAME thread. Both halves must come back to life.
	require.NoError(t, h.ExecuteActivity(activity.Execute, startedInput, &output))

	thread, err := h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.ThreadStatusRunning, thread.Status,
		"a thread executing a new turn must not still read as finished")
	assert.Nil(t, thread.CompletedAt,
		"a revived thread must not keep the completed_at of the turn that ended")

	workflow, err := h.Repo().GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowStatusRunning, workflow.Status)
}

// TestWorkflowStatus_StartedRevivesThreadWhenWorkflowAlreadyRunning covers
// the ordering trap. trackWorkflow returns early when the workflow row is
// already running, so a revival written after that check would be skipped in
// precisely the case where the run is furthest along -- a resumed or
// re-notified run whose workflow never left RUNNING while the thread was
// stamped terminal by a racing completion.
func TestWorkflowStatus_StartedRevivesThreadWhenWorkflowAlreadyRunning(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewWorkflowStatusActivity(h.Repo())
	startedInput := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   chatID,
		WorkflowName: "builtin://chat",
		Status:       "started",
		Thread:       chatID,
	}

	var output WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, startedInput, &output))
	require.NoError(t, h.Repo().UpdateWorkflowStatus(ctx, chatID, db.WorkflowStatusRunning))

	// Only the THREAD is terminal; the workflow row stays running, so the
	// early return is the path under test.
	_, err := h.Repo().UpdateThreadStatus(ctx, chatID, db.ThreadStatusCompleted, nil)
	require.NoError(t, err)

	require.NoError(t, h.ExecuteActivity(activity.Execute, startedInput, &output))

	thread, err := h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.ThreadStatusRunning, thread.Status,
		"the revival must not sit behind trackWorkflow's already-running early return")
}

// TestWorkflowStatus_TerminalStatusDoesNotReviveThread guards the direction
// that would be unrecoverable: only "started" means a run is beginning. A
// completion notification must never resurrect the thread it is closing.
func TestWorkflowStatus_TerminalStatusDoesNotReviveThread(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewWorkflowStatusActivity(h.Repo())
	var output WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://chat",
		Status: "started", Thread: chatID,
	}, &output))

	_, err := h.Repo().UpdateThreadStatus(ctx, chatID, db.ThreadStatusCompleted, nil)
	require.NoError(t, err)

	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://chat",
		Status: "completed", Thread: chatID,
	}, &output))

	thread, err := h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.ThreadStatusCompleted, thread.Status,
		"a terminal notification must never revive the thread it is closing")
}
