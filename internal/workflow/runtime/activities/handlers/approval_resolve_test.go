// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApprovalResolveActivity_Timeout creates a pending approval, then resolves it
// with status "timeout" and verifies the DB record is updated to denied.
func TestApprovalResolveActivity_Timeout(t *testing.T) {
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

	// Pre-create a pending approval directly in the DB
	approvalID := uuid.New().String()
	entityID := workflowID + ":test-activity"
	err := h.Repo().CreateApproval(ctx, &db.Approval{
		ID:                 approvalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:           entityID,
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              "Deploy to production?",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)

	// Execute the resolve activity with timeout status
	activity := NewApprovalResolveActivity(h.Repo())
	input := ApprovalResolveInput{
		ApprovalID: approvalID,
		Status:     "timeout",
	}

	var output ApprovalResolveOutput
	err = h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)
	assert.True(t, output.Success)

	// Verify the approval was updated to denied status in the DB
	approval, err := h.Repo().GetApproval(ctx, approvalID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), approval.Status,
		"Timeout should map to DENIED status")
	require.NotNil(t, approval.ActionTaken)
	assert.Equal(t, "timeout", *approval.ActionTaken)
	require.NotNil(t, approval.ResolvedAt,
		"ResolvedAt should be set after resolving")
}
