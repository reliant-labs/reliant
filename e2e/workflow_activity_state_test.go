// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// WORKFLOW ACTIVITY STATE E2E TESTS
//
// These tests verify the workflow activity state detection rules:
// 1. Main workflow is active if ANY sub-workflow is active
// 2. If main workflow is active, green chat indicator must show
// 3. Individual thread indicators should reflect their specific activity state
// 4. Fork metadata workflows (fork:*) should NOT count as active
//
// These rules ensure the UI shows consistent activity indicators across:
// - Green dot in sidebar (chat is active)
// - Thinking indicator in chat view
// - Thread tab activity indicators
// ============================================================================

// TestWorkflowActivity_RootRunning tests that a chat shows as active when root workflow is running.
func TestWorkflowActivity_RootRunning(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "root-running-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Root Running Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a running root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Query for root workflow status
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status, "root workflow should be running")

	t.Logf("✓ Root workflow running: status=%v", status)
}

// TestWorkflowActivity_RootCompleted tests that a chat shows as inactive when root workflow completes.
func TestWorkflowActivity_RootCompleted(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "root-completed-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Root Completed Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a completed root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusCompleted,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Query for root workflow status
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusCompleted, status, "root workflow should be completed")

	t.Logf("✓ Root workflow completed: status=%v", status)
}

// TestWorkflowActivity_ChildRunning tests that a chat shows as active when ANY child workflow is running.
// This is the key rule: main workflow is active if ANY sub-workflow is active.
func TestWorkflowActivity_ChildRunning(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "child-running-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Child Running Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a completed root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusCompleted, // Root is done...
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create a running child workflow
	childWorkflowID := uuid.New().String()
	childThread := "child-" + uuid.New().String()[:8]
	childWorkflow := &db.Workflow{
		ID:           childWorkflowID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "builtin://agent",
		Thread:       childThread,
		Status:       db.WorkflowStatusRunning, // ...but child is still running
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, childWorkflow)
	require.NoError(t, err)

	// Query for root workflow status - should show "running" because child is running
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status,
		"chat should show as running when ANY child workflow is running")

	t.Logf("✓ Child workflow running propagates to chat: status=%v", status)
}

// TestWorkflowActivity_ParallelChildrenRunning tests that parallel child workflows all contribute to activity.
func TestWorkflowActivity_ParallelChildrenRunning(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "parallel-children-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Parallel Children Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a running root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "thread-demo",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create two parallel child workflows
	for i := 0; i < 2; i++ {
		childWorkflowID := uuid.New().String()
		childThread := "child-" + uuid.New().String()[:8]
		childWorkflow := &db.Workflow{
			ID:           childWorkflowID,
			ChatID:       chatID,
			ParentID:     &rootWorkflowID,
			WorkflowName: "builtin://agent",
			Thread:       childThread,
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now(),
		}
		err = h.DB.CreateWorkflow(ctx, childWorkflow)
		require.NoError(t, err)
	}

	// Query for workflow status
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status,
		"chat should show as running with parallel children")

	t.Logf("✓ Parallel children contribute to activity: status=%v", status)
}

// TestWorkflowActivity_AllChildrenComplete tests that activity clears only when ALL workflows complete.
func TestWorkflowActivity_AllChildrenComplete(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "all-complete-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "All Complete Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a completed root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "thread-demo",
		Thread:       chatID,
		Status:       db.WorkflowStatusCompleted,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create two completed child workflows
	for i := 0; i < 2; i++ {
		childWorkflowID := uuid.New().String()
		childThread := "child-" + uuid.New().String()[:8]
		childWorkflow := &db.Workflow{
			ID:           childWorkflowID,
			ChatID:       chatID,
			ParentID:     &rootWorkflowID,
			WorkflowName: "builtin://agent",
			Thread:       childThread,
			Status:       db.WorkflowStatusCompleted,
			CreatedAt:    time.Now(),
		}
		err = h.DB.CreateWorkflow(ctx, childWorkflow)
		require.NoError(t, err)
	}

	// Query for workflow status - should be completed since all are done
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusCompleted, status,
		"chat should show as completed when ALL workflows are done")

	t.Logf("✓ All workflows complete: status=%v", status)
}

// TestWorkflowActivity_PartialChildComplete tests that one child completing while another runs keeps activity.
func TestWorkflowActivity_PartialChildComplete(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "partial-complete-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Partial Complete Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a running root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "thread-demo",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create one completed child
	completedChildID := uuid.New().String()
	completedChildWorkflow := &db.Workflow{
		ID:           completedChildID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "builtin://agent",
		Thread:       "completed-" + uuid.New().String()[:8],
		Status:       db.WorkflowStatusCompleted, // This one is done
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, completedChildWorkflow)
	require.NoError(t, err)

	// Create one running child
	runningChildID := uuid.New().String()
	runningChildWorkflow := &db.Workflow{
		ID:           runningChildID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "builtin://agent",
		Thread:       "running-" + uuid.New().String()[:8],
		Status:       db.WorkflowStatusRunning, // This one is still running
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, runningChildWorkflow)
	require.NoError(t, err)

	// Query for workflow status - should be running since one child is still running
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status,
		"chat should show as running when ONE child is still running")

	t.Logf("✓ Partial completion maintains activity: status=%v", status)
}

// TestWorkflowActivity_ForkMetadataExcluded tests that thread metadata records don't count as active.
// Thread records ("thread:*" and legacy "fork:*") track thread lifecycle, not workflow execution.
// They complete when their owning workflow completes via cascade.
func TestWorkflowActivity_ForkMetadataExcluded(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "fork-metadata-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Fork Metadata Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a completed root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusCompleted, // Root is completed
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create a "fork:*" metadata workflow that is still "running"
	// This simulates what happens when fork() is called - it creates a metadata record
	forkWorkflowID := uuid.New().String()
	forkThread := "fork-" + uuid.New().String()[:8]
	forkWorkflow := &db.Workflow{
		ID:           forkWorkflowID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "fork:" + forkThread, // Fork metadata workflow name
		Thread:       forkThread,
		Status:       db.WorkflowStatusRunning, // Fork workflows don't complete
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, forkWorkflow)
	require.NoError(t, err)

	// Query for workflow status - should NOT be running because fork:* is excluded
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusCompleted, status,
		"chat should show as completed - thread metadata records should be excluded from activity detection")

	// Also test with "thread:*" naming (new format)
	threadWorkflowID := uuid.New().String()
	threadRecordThread := "thread-" + uuid.New().String()[:8]
	threadRecord := &db.Workflow{
		ID:           threadWorkflowID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "thread:" + threadRecordThread, // New thread record naming
		Thread:       threadRecordThread,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, threadRecord)
	require.NoError(t, err)

	// Status should still be completed (thread:* also excluded)
	statusMap, err = h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)
	status, exists = statusMap[chatID]
	require.True(t, exists)
	assert.Equal(t, db.WorkflowStatusCompleted, status,
		"chat should show as completed - thread:* records should also be excluded")

	t.Logf("✓ Thread metadata excluded from activity: status=%v (both fork:* and thread:* excluded)", status)
}

// TestWorkflowActivity_ForkMetadataWithRealChild tests thread metadata is excluded while real children are counted.
func TestWorkflowActivity_ForkMetadataWithRealChild(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "fork-with-child-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Fork With Child Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a running root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Create a fork metadata workflow (should be ignored)
	forkWorkflowID := uuid.New().String()
	forkThread := "fork-" + uuid.New().String()[:8]
	forkWorkflow := &db.Workflow{
		ID:           forkWorkflowID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "fork:" + forkThread,
		Thread:       forkThread,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, forkWorkflow)
	require.NoError(t, err)

	// Create a real running child workflow
	realChildID := uuid.New().String()
	realChildWorkflow := &db.Workflow{
		ID:           realChildID,
		ChatID:       chatID,
		ParentID:     &rootWorkflowID,
		WorkflowName: "builtin://agent", // Real workflow, not fork:*
		Thread:       "real-child-" + uuid.New().String()[:8],
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, realChildWorkflow)
	require.NoError(t, err)

	// Query for workflow status - should be running due to real child
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status,
		"chat should show as running due to real child workflow")

	t.Logf("✓ Real child counted, fork excluded: status=%v", status)
}

// TestWorkflowActivity_CancelledWorkflow tests that cancelled workflows don't show as active.
func TestWorkflowActivity_CancelledWorkflow(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "cancelled-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Cancelled Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a cancelled root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusCancelled,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Query for workflow status
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusCancelled, status,
		"cancelled workflow should not show as active")

	t.Logf("✓ Cancelled workflow not active: status=%v", status)
}

// TestWorkflowActivity_FailedWorkflow tests that failed workflows don't show as active.
func TestWorkflowActivity_FailedWorkflow(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "failed-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Failed Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a failed root workflow
	rootWorkflowID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootWorkflowID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusFailed,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Query for workflow status
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusFailed, status,
		"failed workflow should not show as active")

	t.Logf("✓ Failed workflow not active: status=%v", status)
}

// TestWorkflowActivity_MultipleChats tests querying activity state for multiple chats at once.
func TestWorkflowActivity_MultipleChats(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Create one project with three chats having different workflow states
	project := h.CreateProjectOnly(t, "multi-chat-test")

	chatID1 := uuid.New().String()
	chat1 := &db.Chat{
		ID:        chatID1,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Running Chat",
	}
	err := h.DB.CreateChat(ctx, chat1)
	require.NoError(t, err)

	chatID2 := uuid.New().String()
	chat2 := &db.Chat{
		ID:        chatID2,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Completed Chat",
	}
	err = h.DB.CreateChat(ctx, chat2)
	require.NoError(t, err)

	chatID3 := uuid.New().String()
	chat3 := &db.Chat{
		ID:        chatID3,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "No Workflow Chat",
	}
	err = h.DB.CreateChat(ctx, chat3)
	require.NoError(t, err)

	// Chat 1: Running workflow
	workflow1 := &db.Workflow{
		ID:           uuid.New().String(),
		ChatID:       chatID1,
		WorkflowName: "builtin://agent",
		Thread:       chatID1,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, workflow1)
	require.NoError(t, err)

	// Chat 2: Completed workflow
	workflow2 := &db.Workflow{
		ID:           uuid.New().String(),
		ChatID:       chatID2,
		WorkflowName: "builtin://agent",
		Thread:       chatID2,
		Status:       db.WorkflowStatusCompleted,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, workflow2)
	require.NoError(t, err)

	// Chat 3: No workflow

	// Query for all three chats
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID1, chatID2, chatID3})
	require.NoError(t, err)

	// Verify statuses
	assert.Equal(t, db.WorkflowStatusRunning, statusMap[chatID1], "chat1 should be running")
	assert.Equal(t, db.WorkflowStatusCompleted, statusMap[chatID2], "chat2 should be completed")
	_, exists := statusMap[chatID3]
	assert.False(t, exists, "chat3 should have no status (no workflow)")

	t.Logf("✓ Multiple chats queried correctly: running=%d, completed=%d, no-status=%d",
		len(statusMap), len(statusMap), 3-len(statusMap))
}

// TestWorkflowActivity_NestedHierarchy tests deeply nested workflow hierarchies.
func TestWorkflowActivity_NestedHierarchy(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "nested-hierarchy-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Nested Hierarchy Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create root -> child -> grandchild hierarchy
	rootID := uuid.New().String()
	childID := uuid.New().String()
	grandchildID := uuid.New().String()

	// Root: completed
	rootWorkflow := &db.Workflow{
		ID:           rootID,
		ChatID:       chatID,
		WorkflowName: "thread-demo",
		Thread:       chatID,
		Status:       db.WorkflowStatusCompleted,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Child: completed
	childWorkflow := &db.Workflow{
		ID:           childID,
		ChatID:       chatID,
		ParentID:     &rootID,
		WorkflowName: "builtin://agent",
		Thread:       "child-" + uuid.New().String()[:8],
		Status:       db.WorkflowStatusCompleted,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, childWorkflow)
	require.NoError(t, err)

	// Grandchild: still running
	grandchildWorkflow := &db.Workflow{
		ID:           grandchildID,
		ChatID:       chatID,
		ParentID:     &childID,
		WorkflowName: "builtin://agent",
		Thread:       "grandchild-" + uuid.New().String()[:8],
		Status:       db.WorkflowStatusRunning, // Deep nested still running
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, grandchildWorkflow)
	require.NoError(t, err)

	// Query for workflow status - should be running due to grandchild
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)

	status, exists := statusMap[chatID]
	require.True(t, exists, "should have status for chat")
	assert.Equal(t, db.WorkflowStatusRunning, status,
		"chat should show as running due to deeply nested grandchild workflow")

	t.Logf("✓ Nested hierarchy propagates activity: status=%v", status)
}

// TestWorkflowActivity_TransitionSequence tests the full lifecycle of workflow activity state.
func TestWorkflowActivity_TransitionSequence(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateProjectOnly(t, "transition-test")
	chatID := uuid.New().String()
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: project.ID,
		UserID:    h.UserID(),
		Title:     "Transition Test",
	}
	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err)

	// Create a running root workflow
	rootID := uuid.New().String()
	rootWorkflow := &db.Workflow{
		ID:           rootID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
		CreatedAt:    time.Now(),
	}
	err = h.DB.CreateWorkflow(ctx, rootWorkflow)
	require.NoError(t, err)

	// Initially running
	statusMap, err := h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowStatusRunning, statusMap[chatID], "should start as running")

	// Transition to completed
	err = h.DB.UpdateWorkflowStatus(ctx, rootID, db.WorkflowStatusCompleted)
	require.NoError(t, err)

	// Verify completed
	statusMap, err = h.DB.GetRootWorkflowStatusForChats(ctx, []string{chatID})
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowStatusCompleted, statusMap[chatID], "should transition to completed")

	t.Logf("✓ Workflow transition sequence: running -> completed")
}
