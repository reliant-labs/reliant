// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApprovalCreateActivity_Basic creates an approval and verifies the output
// contains the approval_id and already_resolved=false.
func TestApprovalCreateActivity_Basic(t *testing.T) {
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

	activity := NewApprovalCreateActivity(h.Repo())

	input := ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}

	var output ApprovalCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.NotEmpty(t, output.ApprovalID)
	assert.False(t, output.AlreadyResolved)
}

// TestApprovalCreateActivity_MarksChatUnread verifies that when a new approval is created,
// the chat is marked as unread so the UI shows a notification badge.
func TestApprovalCreateActivity_MarksChatUnread(t *testing.T) {
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

	activity := NewApprovalCreateActivity(h.Repo())

	input := ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}

	var output ApprovalCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.NotEmpty(t, output.ApprovalID)

	// Verify chat was marked as unread
	chat, err := h.Repo().GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, chat.Unread,
		"Chat should be marked as unread after approval creation")
}

// TestApprovalCreateActivity_EmitsApprovalUpdate verifies that creating an approval emits
// a chat_update with type "approval" and status "pending".
func TestApprovalCreateActivity_EmitsApprovalUpdate(t *testing.T) {
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

	activity := NewApprovalCreateActivity(h.Repo())

	input := ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}

	var output ApprovalCreateOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.NotEmpty(t, output.ApprovalID)

	// Query chat_updates table to verify the approval update was emitted
	var count int
	err = h.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_updates WHERE chat_id = $1 AND update_type = $2`,
		chatID, int32(db.UpdateTypeApproval),
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "Expected exactly 1 approval chat_update to be emitted")
}

// TestApprovalCreateActivity_Idempotency verifies that calling ApprovalCreate with a
// pre-existing pending approval for the same entity ID returns the existing approval
// rather than creating a duplicate.
func TestApprovalCreateActivity_Idempotency(t *testing.T) {
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

	// The entity ID is built as "<workflowExecID>:<activityID>".
	// In the Temporal test environment, workflowExecID = "default-test-workflow-id"
	// and activityID is an auto-incrementing counter. We pre-create a pending approval
	// with the entity ID that the next ExecuteActivity call will use.
	// The first call to ExecuteActivity with a fresh env gets activityID "0".
	entityID := "default-test-workflow-id:0"
	existingApprovalID := uuid.New().String()
	err := h.Repo().CreateApproval(ctx, &db.Approval{
		ID:                 existingApprovalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:           entityID,
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              "Deploy to production?",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)

	activity := NewApprovalCreateActivity(h.Repo())
	input := ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}

	var output ApprovalCreateOutput
	err = h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// Should return the existing approval, not create a new one
	assert.Equal(t, existingApprovalID, output.ApprovalID)
	assert.False(t, output.AlreadyResolved)
}

// TestApprovalCreateActivity_IdempotencyAlreadyResolved verifies that if the existing
// approval is already resolved (approved/denied), ApprovalCreate returns
// AlreadyResolved=true with the correct status and action_taken.
func TestApprovalCreateActivity_IdempotencyAlreadyResolved(t *testing.T) {
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

	// Pre-create an already-resolved approval with the entity ID the test env will use.
	entityID := "default-test-workflow-id:0"
	existingApprovalID := uuid.New().String()
	actionTaken := "deploy"
	err := h.Repo().CreateApproval(ctx, &db.Approval{
		ID:                 existingApprovalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:           entityID,
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              "Deploy to production?",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)

	// Resolve the approval
	err = h.Repo().UpdateApprovalStatus(ctx, existingApprovalID,
		int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED),
		nil, &actionTaken, nil)
	require.NoError(t, err)

	activity := NewApprovalCreateActivity(h.Repo())
	input := ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}

	var output ApprovalCreateOutput
	err = h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	assert.Equal(t, existingApprovalID, output.ApprovalID)
	assert.True(t, output.AlreadyResolved, "Should report approval as already resolved")
	assert.Equal(t, "approved", output.Status)
	assert.Equal(t, "deploy", output.ActionTaken)
}