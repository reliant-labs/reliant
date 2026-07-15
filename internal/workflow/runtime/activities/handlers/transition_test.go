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
// TESTS FOR transition_to (transition-chat-to-workflow on completion)
// ============================================================================
//
// When a ROOT workflow run COMPLETES and its definition declares transition_to,
// the chat permanently switches to that workflow so the user can keep talking
// after a one-shot pipeline ends. builtin://forge-one-shot declares
// transition_to: builtin://agent, and builtin://agent declares none — so the
// chain terminates (no cycle).
// ============================================================================

const (
	oneShotWF = "builtin://forge-one-shot"
	agentWF   = "builtin://agent"
)

// setChatWorkflow points a test chat at a given workflow name.
func setChatWorkflow(t *testing.T, ctx context.Context, repo db.Repository, chatID, workflowName string) {
	t.Helper()
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	chat.WorkflowName = ptr.Of(workflowName)
	require.NoError(t, repo.UpdateChat(ctx, chat))
}

func chatWorkflowName(t *testing.T, ctx context.Context, repo db.Repository, chatID string) string {
	t.Helper()
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.WorkflowName)
	return *chat.WorkflowName
}

// TestWorkflowStatus_TransitionToOnCompletion verifies a completed root workflow
// switches the chat to its declared transition_to target, atomically with the
// status write, and idempotently.
func TestWorkflowStatus_TransitionToOnCompletion(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	setChatWorkflow(t, ctx, h.Repo(), chatID, oneShotWF)

	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: oneShotWF,
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}))

	activity := NewWorkflowStatusActivity(h.Repo())
	input := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   workflowID,
		WorkflowName: oneShotWF,
		Status:       "completed",
		Thread:       chatID,
		// ParentWorkflowID empty => root workflow
	}

	t.Run("root completion switches chat to transition_to target", func(t *testing.T) {
		var out WorkflowStatusOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, input, &out))
		assert.True(t, out.Success)

		// Status write committed.
		wf, err := h.Repo().GetWorkflow(ctx, workflowID)
		require.NoError(t, err)
		assert.Equal(t, db.WorkflowStatusCompleted, wf.Status)

		// Chat graduated to the target workflow.
		assert.Equal(t, agentWF, chatWorkflowName(t, ctx, h.Repo(), chatID))
	})

	t.Run("re-running completion is idempotent (already transitioned)", func(t *testing.T) {
		var out WorkflowStatusOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, input, &out))
		assert.True(t, out.Success)
		// Still on the target; not re-switched or cycled.
		assert.Equal(t, agentWF, chatWorkflowName(t, ctx, h.Repo(), chatID))
	})
}

// TestWorkflowStatus_NoTransitionWhenTargetAbsent verifies a completed workflow
// with no transition_to (builtin://agent) leaves the chat's workflow untouched —
// this also proves the transition chain terminates (no cycle).
func TestWorkflowStatus_NoTransitionWhenTargetAbsent(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	workflowID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	setChatWorkflow(t, ctx, h.Repo(), chatID, agentWF)

	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: agentWF,
		Thread:       chatID,
		Status:       db.WorkflowStatusRunning,
	}))

	activity := NewWorkflowStatusActivity(h.Repo())
	var out WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   workflowID,
		WorkflowName: agentWF,
		Status:       "completed",
		Thread:       chatID,
	}, &out))
	assert.True(t, out.Success)

	assert.Equal(t, agentWF, chatWorkflowName(t, ctx, h.Repo(), chatID),
		"chat with no transition_to target must stay on its workflow (chain terminates)")
}

// TestWorkflowStatus_ChildCompletionDoesNotTransition verifies that only the
// chat's ACTIVE ROOT workflow transitions — a completing child (spawn/fork) with
// a transition_to-bearing name must never switch the chat out from under its
// parent.
func TestWorkflowStatus_ChildCompletionDoesNotTransition(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	parentID := uuid.New().String()
	childID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	setChatWorkflow(t, ctx, h.Repo(), chatID, oneShotWF)

	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: parentID, ChatID: chatID, WorkflowName: oneShotWF, Thread: parentID, Status: db.WorkflowStatusRunning,
	}))
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: childID, ParentID: &parentID, ChatID: chatID, WorkflowName: oneShotWF, Thread: childID, Status: db.WorkflowStatusRunning,
	}))

	activity := NewWorkflowStatusActivity(h.Repo())
	var out WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID:           chatID,
		WorkflowID:       childID,
		WorkflowName:     oneShotWF,
		Status:           "completed",
		Thread:           childID,
		ParentWorkflowID: parentID,
	}, &out))
	assert.True(t, out.Success)

	assert.Equal(t, oneShotWF, chatWorkflowName(t, ctx, h.Repo(), chatID),
		"a completing child workflow must not transition the chat")
}

// TestTransitionChatOnCompletion_Guards exercises the helper's guards directly.
func TestTransitionChatOnCompletion_Guards(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	t.Run("no-op when chat is not on the completed workflow", func(t *testing.T) {
		setChatWorkflow(t, ctx, h.Repo(), chatID, agentWF)
		// forge-one-shot completes but the chat already moved on to agent.
		to, err := TransitionChatOnCompletion(ctx, h.Repo(), chatID, oneShotWF)
		require.NoError(t, err)
		assert.Empty(t, to)
		assert.Equal(t, agentWF, chatWorkflowName(t, ctx, h.Repo(), chatID))
	})

	t.Run("switches when chat is on the completed workflow", func(t *testing.T) {
		setChatWorkflow(t, ctx, h.Repo(), chatID, oneShotWF)
		to, err := TransitionChatOnCompletion(ctx, h.Repo(), chatID, oneShotWF)
		require.NoError(t, err)
		assert.Equal(t, agentWF, to)
		assert.Equal(t, agentWF, chatWorkflowName(t, ctx, h.Repo(), chatID))
	})

	t.Run("no-op for a completed workflow with no transition_to", func(t *testing.T) {
		setChatWorkflow(t, ctx, h.Repo(), chatID, agentWF)
		to, err := TransitionChatOnCompletion(ctx, h.Repo(), chatID, agentWF)
		require.NoError(t, err)
		assert.Empty(t, to)
	})
}
