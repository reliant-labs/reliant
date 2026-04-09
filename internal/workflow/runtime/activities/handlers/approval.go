// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// ApprovalCreateInput is the input for the ApprovalCreate activity.
type ApprovalCreateInput struct {
	ChatID             string `json:"chat_id" reliant:"-"`
	WorkflowID         string `json:"workflow_id" reliant:"-"`
	TemporalWorkflowID string `json:"temporal_workflow_id" reliant:"-"`
	StepID             string `json:"step_id" reliant:"-"`
	Title              string `json:"title" reliant:"-"`
	Timeout            string `json:"timeout,omitempty" reliant:"-"` // Duration string, e.g. "1h"
}

// ApprovalCreateOutput is the output from the ApprovalCreate activity.
type ApprovalCreateOutput struct {
	ApprovalID      string `json:"approval_id"`
	AlreadyResolved bool   `json:"already_resolved"`
	Status          string `json:"status"`       // Only set if AlreadyResolved
	ActionTaken     string `json:"action_taken"` // Only set if AlreadyResolved
}

// ApprovalResolveInput is the input for the ApprovalResolve activity.
type ApprovalResolveInput struct {
	ApprovalID string `json:"approval_id" reliant:"-"`
	Status     string `json:"status" reliant:"-"` // "timeout"
}

// ApprovalResolveOutput is the output from the ApprovalResolve activity.
type ApprovalResolveOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY: ApprovalCreateActivity
// ============================================================================

// ApprovalCreateActivity creates an approval record in the DB and returns immediately.
// The workflow then waits on a signal channel for resolution — no polling needed.
type ApprovalCreateActivity struct {
	repo db.Repository
}

// NewApprovalCreateActivity creates a new ApprovalCreateActivity
func NewApprovalCreateActivity(repo db.Repository) *ApprovalCreateActivity {
	return &ApprovalCreateActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *ApprovalCreateActivity) Name() string {
	return "ApprovalCreate"
}

// DisplayName returns human-readable name for UI
func (a *ApprovalCreateActivity) DisplayName() string {
	return "ApprovalCreate"
}

// Description returns what the activity does
func (a *ApprovalCreateActivity) Description() string {
	return "Create approval record and return immediately (signal-based)"
}

// Category returns the activity category for UI grouping
func (a *ApprovalCreateActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute creates the approval record. If an approval already exists for this
// entity ID (idempotency), it returns the existing one. If the existing approval
// is already resolved, AlreadyResolved=true is returned so the workflow can skip
// signal waiting.
func (a *ApprovalCreateActivity) Execute(ctx context.Context, input ApprovalCreateInput) (ApprovalCreateOutput, error) {
	logger := activity.GetLogger(ctx)

	// Get activity info from Temporal for idempotency key
	activityInfo := activity.GetInfo(ctx)
	activityID := activityInfo.ActivityID
	workflowID := activityInfo.WorkflowExecution.ID

	// Build a unique entity ID that includes the workflow ID to prevent cross-workflow collisions.
	entityID := fmt.Sprintf("%s:%s", workflowID, activityID)

	logger.Info("[ApprovalCreate] Creating approval record",
		"chatID", input.ChatID,
		"title", input.Title,
		"workflowID", workflowID,
		"entityID", entityID)

	// IDEMPOTENCY: Check if we already created this approval using the workflow-scoped entity ID.
	existingApproval, err := a.repo.GetApprovalByEntityID(ctx, entityID)
	if err == nil && existingApproval != nil {
		logger.Info("[ApprovalCreate] Found existing approval",
			"approvalID", existingApproval.ID,
			"entityID", entityID,
			"status", existingApproval.Status)

		if existingApproval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
			// Already resolved — return status so workflow can skip signal waiting
			status := approvalStatusString(existingApproval.Status)
			actionTaken := ""
			if existingApproval.ActionTaken != nil {
				actionTaken = *existingApproval.ActionTaken
			}
			return ApprovalCreateOutput{
				ApprovalID:      existingApproval.ID,
				AlreadyResolved: true,
				Status:          status,
				ActionTaken:     actionTaken,
			}, nil
		}

		// Still pending — return approval ID so workflow waits on signal
		return ApprovalCreateOutput{
			ApprovalID:      existingApproval.ID,
			AlreadyResolved: false,
		}, nil
	}

	// Generate approval ID and create record
	approvalID := uuid.New().String()

	// Build metadata with workflow context for signaling
	metadata := map[string]interface{}{}
	if workflowID != "" {
		metadata["workflow_id"] = workflowID
		metadata["run_id"] = activityInfo.WorkflowExecution.RunID
	}
	var metadataJSON *string
	if len(metadata) > 0 {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return ApprovalCreateOutput{}, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataStr := string(metadataBytes)
		metadataJSON = &metadataStr
	}

	// Resolve temporal workflow ID: use provided value, fall back to WorkflowID (root workflows)
	temporalWorkflowID := input.TemporalWorkflowID
	if temporalWorkflowID == "" {
		temporalWorkflowID = input.WorkflowID
	}

	approval := &db.Approval{
		ID:                 approvalID,
		ChatID:             input.ChatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:           entityID,
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              input.Title,
		Metadata:           metadataJSON,
		TemporalWorkflowID: temporalWorkflowID,
		CreatedAt:          time.Now().UTC(),
		ResolvedAt:         nil,
	}

	// Dual-write: create approval record + chat_update + mark chat unread atomically
	err = a.repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := a.repo.CreateApproval(txCtx, approval); err != nil {
			return fmt.Errorf("failed to create approval: %w", err)
		}

		// Mark chat as unread so UI shows notification badge
		if err := a.repo.UpdateChatUnread(txCtx, input.ChatID, true, "approval_required"); err != nil {
			logger.Warn("[ApprovalCreate] Failed to mark chat as unread",
				"error", err,
				"chatID", input.ChatID)
		}

		// Build chat_update data
		updateData := map[string]interface{}{
			"update_type":   "approval",
			"id":            approvalID,
			"approval_type": "workflow_step",
			"activity_id":   activityID,
			"status":        "pending",
			"title":         input.Title,
			"created_at":    approval.CreatedAt.Format(time.RFC3339),
		}

		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := a.repo.CreateChatUpdate(txCtx, input.ChatID, db.UpdateTypeApproval, approvalID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})
	if err != nil {
		return ApprovalCreateOutput{}, err
	}

	logger.Info("[ApprovalCreate] Approval created successfully",
		"approvalID", approvalID,
		"chatID", input.ChatID)

	return ApprovalCreateOutput{
		ApprovalID:      approvalID,
		AlreadyResolved: false,
	}, nil
}

// ============================================================================
// ACTIVITY: ApprovalResolveActivity
// ============================================================================

// ApprovalResolveActivity resolves an approval record in the DB. Used by the workflow
// when the signal timer expires (timeout case).
type ApprovalResolveActivity struct {
	repo db.Repository
}

// NewApprovalResolveActivity creates a new ApprovalResolveActivity
func NewApprovalResolveActivity(repo db.Repository) *ApprovalResolveActivity {
	return &ApprovalResolveActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *ApprovalResolveActivity) Name() string {
	return "ApprovalResolve"
}

// DisplayName returns human-readable name for UI
func (a *ApprovalResolveActivity) DisplayName() string {
	return "ApprovalResolve"
}

// Description returns what the activity does
func (a *ApprovalResolveActivity) Description() string {
	return "Resolve an approval record in the database (timeout cleanup)"
}

// Category returns the activity category for UI grouping
func (a *ApprovalResolveActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute resolves the approval record with the given status.
func (a *ApprovalResolveActivity) Execute(ctx context.Context, input ApprovalResolveInput) (ApprovalResolveOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[ApprovalResolve] Resolving approval",
		"approvalID", input.ApprovalID,
		"status", input.Status)

	// Map string status to proto enum
	var statusEnum int32
	switch input.Status {
	case "timeout":
		statusEnum = int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED)
	default:
		statusEnum = int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED)
	}

	actionTaken := input.Status
	if err := a.repo.UpdateApprovalStatus(ctx, input.ApprovalID, statusEnum, nil, &actionTaken, nil); err != nil {
		return ApprovalResolveOutput{}, fmt.Errorf("failed to resolve approval: %w", err)
	}

	return ApprovalResolveOutput{Success: true}, nil
}

// ============================================================================
// HELPERS
// ============================================================================

// approvalStatusString converts a proto approval status to a string.
func approvalStatusString(status int32) string {
	switch status {
	case int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED):
		return "approved"
	case int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED):
		return "denied"
	case int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING):
		return "pending"
	default:
		return "unknown"
	}
}
