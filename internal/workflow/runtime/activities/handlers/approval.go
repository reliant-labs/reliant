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
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// approvalFields holds resolved approval fields extracted from the proto node.
// This avoids passing the full ActivityInput through internal methods.
type approvalFields struct {
	ChatID  string
	Title   string
	Timeout string
}

// ============================================================================
// ACTIVITY: ApprovalActivity
// ============================================================================

// ApprovalActivity implements the approval activity.
// This consolidated activity creates an approval record and polls for its status.
type ApprovalActivity struct {
	repo db.Repository
}

// NewApprovalActivity creates a new ApprovalActivity
func NewApprovalActivity(repo db.Repository) *ApprovalActivity {
	return &ApprovalActivity{
		repo: repo,
	}
}

// Name returns the activity name for registration
func (a *ApprovalActivity) Name() string {
	return "Approval"
}

// DisplayName returns human-readable name for UI
func (a *ApprovalActivity) DisplayName() string {
	return "Approval"
}

// Description returns what the activity does
func (a *ApprovalActivity) Description() string {
	return "Pause workflow and wait for user approval before continuing"
}

// Category returns the activity category for UI grouping
func (a *ApprovalActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute contains PURE BUSINESS LOGIC only
func (a *ApprovalActivity) Execute(ctx context.Context, input ActivityInput) (ApprovalOutput, error) {
	rtx := input.Runtime
	protoArgs := model.GetApprovalArgs(input.Node)
	if protoArgs == nil {
		return ApprovalOutput{}, fmt.Errorf("expected approval node, got %s", model.NodeType(input.Node))
	}

	// Extract resolved fields from proto args
	fields := approvalFields{
		ChatID:  rtx.ChatID,
		Title:   model.CelStringValue(protoArgs.GetTitle()),
		Timeout: model.CelStringValue(protoArgs.GetTimeout()),
	}

	// Get activity info from Temporal - ActivityID is unique per activity execution
	// and handles idempotency automatically via Temporal's retry mechanism
	activityInfo := activity.GetInfo(ctx)
	activityID := activityInfo.ActivityID
	workflowID := activityInfo.WorkflowExecution.ID

	// Build a unique entity ID that includes the workflow ID to prevent cross-workflow collisions.
	// Temporal activity IDs are sequential within a workflow (e.g., 5, 11, 17...) but different
	// workflows can have the same activity IDs. Without the workflow ID prefix, we could
	// incorrectly reuse an approval from a completely different workflow.
	entityID := fmt.Sprintf("%s:%s", workflowID, activityID)

	activity.GetLogger(ctx).Info("[ApprovalActivity] ========== EXECUTE STARTED ==========",
		"chatID", fields.ChatID,
		"title", fields.Title,
		"activityID", activityID,
		"workflowID", workflowID,
		"entityID", entityID)

	// IDEMPOTENCY: Check if we already created this approval using the workflow-scoped entity ID
	// This handles Temporal activity retries - if we crashed after creating the approval
	// but before returning, we'll find it here on retry
	existingApproval, err := a.repo.GetApprovalByEntityID(ctx, entityID)
	if err == nil && existingApproval != nil {
		// Approval already exists from previous attempt
		// Jump directly to polling with existing approval
		activity.GetLogger(ctx).Info("[ApprovalActivity] Found existing approval from previous attempt",
			"approvalID", existingApproval.ID,
			"entityID", entityID,
			"status", existingApproval.Status)
		return a.pollApproval(ctx, existingApproval, fields)
	}

	// STEP 1: Generate approval ID
	approvalID := uuid.New().String()
	activity.GetLogger(ctx).Info("[APPROVAL_CREATE] ========== GENERATING APPROVAL ID ==========",
		"approval_id", approvalID,
		"chat_id", fields.ChatID,
		"title", fields.Title)

	// STEP 2: Build metadata with workflow context for signaling
	metadata := map[string]interface{}{}
	if activityInfo.WorkflowExecution.ID != "" {
		metadata["workflow_id"] = activityInfo.WorkflowExecution.ID
		metadata["run_id"] = activityInfo.WorkflowExecution.RunID
	}
	var metadataJSON *string
	if len(metadata) > 0 {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return ApprovalOutput{}, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataStr := string(metadataBytes)
		metadataJSON = &metadataStr
	}

	// STEP 3: Create approval record
	// EntityID uses workflowID:activityID for idempotency (prevents cross-workflow collisions)
	approval := &db.Approval{
		ID:           approvalID,
		ChatID:       fields.ChatID,
		ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:     entityID,
		Status:       int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:        fields.Title,
		Metadata:     metadataJSON,
		CreatedAt:    time.Now().UTC(),
		ResolvedAt:   nil,
	}

	// STEP 6: Dual-write - Create approval record AND chat_update atomically
	err = a.repo.RunTx(ctx, func(txCtx context.Context) error {
		// Create approval record
		if err := a.repo.CreateApproval(txCtx, approval); err != nil {
			return fmt.Errorf("failed to create approval: %w", err)
		}
		activity.GetLogger(ctx).Info("[APPROVAL_CREATE] Successfully wrote approval to database",
			"approval_id", approvalID,
			"chat_id", fields.ChatID,
			"activity_id", activityID,
			"status", "pending")

		// Mark chat as unread when approval is required
		// This ensures the UI shows a notification badge for user action
		if err := a.repo.UpdateChatUnread(txCtx, fields.ChatID, true, "approval_required"); err != nil {
			// Log but don't fail - the approval creation is more important
			activity.GetLogger(ctx).Warn("[APPROVAL_CREATE] Failed to mark chat as unread",
				"error", err,
				"chat_id", fields.ChatID)
		}

		// Build chat_update data
		updateData := map[string]interface{}{
			"update_type":   "approval",
			"id":            approvalID,
			"approval_type": "workflow_step",
			"activity_id":   activityID,
			"status":        "pending",
			"title":         fields.Title,
			"created_at":    approval.CreatedAt.Format(time.RFC3339),
		}

		// Marshal update data
		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		// Create chat_update in same transaction
		if err := a.repo.CreateChatUpdate(txCtx, fields.ChatID, db.UpdateTypeApproval, approvalID, string(updateDataJSON)); err != nil {
			activity.GetLogger(ctx).Error("[DUAL_WRITE] Failed to create chat_update",
				"error", err,
				"chat_id", fields.ChatID,
				"approval_id", approvalID)
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		activity.GetLogger(ctx).Error("[ApprovalActivity] FAILED to create approval",
			"error", err,
			"approvalID", approvalID,
			"chatID", fields.ChatID)
		return ApprovalOutput{}, err
	}

	activity.GetLogger(ctx).Info("[ApprovalActivity] Approval created successfully, starting polling",
		"approvalID", approvalID,
		"chatID", fields.ChatID,
		"status", "pending")

	// STEP 7: Poll for approval status
	return a.pollApproval(ctx, approval, fields)
}

// pollApproval polls the database for approval status until resolved or timeout
func (a *ApprovalActivity) pollApproval(ctx context.Context, approval *db.Approval, fields approvalFields) (ApprovalOutput, error) {
	// Parse timeout (from fields or default to 1 hour)
	timeout := 1 * time.Hour
	if fields.Timeout != "" {
		parsedTimeout, err := time.ParseDuration(fields.Timeout)
		if err == nil {
			timeout = parsedTimeout
		}
	}

	// Calculate deadline
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ApprovalOutput{}, ctx.Err()

		case <-ticker.C:
			// Check if we've exceeded timeout
			if time.Now().After(deadline) {
				return ApprovalOutput{
					ApprovalId:  approval.ID,
					Status:      "timeout",
					ActionTaken: "",
					Data:        goMapToProtoStruct(a.buildOutputData(approval, "timeout", nil)),
				}, nil
			}

			// NOTE: Auto-approve mode switching is now handled via workflow signals.
			// When user switches to agent mode, UpdateWorkflowParams signals the workflow,
			// which updates the approval status directly.

			// Query current approval status
			currentApproval, err := a.repo.GetApproval(ctx, approval.ID)
			if err != nil {
				return ApprovalOutput{}, fmt.Errorf("failed to get approval status: %w", err)
			}

			if currentApproval == nil {
				return ApprovalOutput{}, fmt.Errorf("approval not found: %s", approval.ID)
			}

			// Check if approval is resolved
			if currentApproval.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED) {
				actionTaken := ""
				if currentApproval.ActionTaken != nil {
					actionTaken = *currentApproval.ActionTaken
				}
				return ApprovalOutput{
					ApprovalId:  approval.ID,
					Status:      "approved",
					ActionTaken: actionTaken,
					Data:        goMapToProtoStruct(a.buildOutputData(currentApproval, "approved", nil)),
				}, nil
			}

			if currentApproval.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) {
				actionTaken := ""
				if currentApproval.ActionTaken != nil {
					actionTaken = *currentApproval.ActionTaken
				}
				return ApprovalOutput{
					ApprovalId:  approval.ID,
					Status:      "denied",
					ActionTaken: actionTaken,
					Data:        goMapToProtoStruct(a.buildOutputData(currentApproval, "denied", currentApproval.DenialReason)),
				}, nil
			}

			// Still pending, continue polling
		}
	}
}

// buildOutputData creates the output data map from approval record
func (a *ApprovalActivity) buildOutputData(approval *db.Approval, status string, denialReason *string) map[string]interface{} {
	data := map[string]interface{}{
		"approval_id": approval.ID,
		"status":      status,
		"title":       approval.Title,
	}

	if denialReason != nil {
		data["denial_reason"] = *denialReason
	}

	// Include action_taken if present
	if approval.ActionTaken != nil {
		data["action_taken"] = *approval.ActionTaken
	}

	return data
}
