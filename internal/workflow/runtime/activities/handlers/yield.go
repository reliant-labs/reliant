// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES
// ============================================================================

// YieldCreateInput is the input for the YieldCreate activity.
type YieldCreateInput struct {
	ChatID             string  `json:"chat_id" reliant:"-"`
	WorkflowID         string  `json:"workflow_id" reliant:"-"`
	TemporalWorkflowID string  `json:"temporal_workflow_id" reliant:"-"` // The actual Temporal execution ID for signaling
	ThreadID           string  `json:"thread_id" reliant:"-"`
	StepID             string  `json:"step_id" reliant:"-"`
	LoopNodeID         string  `json:"loop_node_id,omitempty" reliant:"-"`
	LoopIteration      int     `json:"loop_iteration" reliant:"-"`
	Metadata           *string `json:"metadata,omitempty" reliant:"-"`
	ToolCallID         string  `json:"tool_call_id,omitempty" reliant:"-"` // Disambiguates multiple ask_user yields in the same iteration
}

// YieldCreateOutput is the output from the YieldCreate activity.
type YieldCreateOutput struct {
	YieldID         string `json:"yield_id"`
	AlreadyResolved bool   `json:"already_resolved"`
	ActionTaken     string `json:"action_taken"` // Only set if AlreadyResolved
}

// YieldResolveInput is the input for the YieldResolve activity.
type YieldResolveInput struct {
	YieldID string `json:"yield_id" reliant:"-"`
	Action  string `json:"action" reliant:"-"`
}

// YieldResolveOutput is the output from the YieldResolve activity.
type YieldResolveOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY: YieldCreateActivity
// ============================================================================

// YieldCreateActivity creates a yield record in the DB and returns immediately.
// The workflow then waits on a signal channel for resolution — no polling needed.
type YieldCreateActivity struct {
	repo db.Repository
}

// NewYieldCreateActivity creates a new YieldCreateActivity
func NewYieldCreateActivity(repo db.Repository) *YieldCreateActivity {
	return &YieldCreateActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *YieldCreateActivity) Name() string {
	return "YieldCreate"
}

// DisplayName returns human-readable name for UI
func (a *YieldCreateActivity) DisplayName() string {
	return "YieldCreate"
}

// Description returns what the activity does
func (a *YieldCreateActivity) Description() string {
	return "Create yield record and return immediately (signal-based)"
}

// Category returns the activity category for UI grouping
func (a *YieldCreateActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute creates the yield record. If a yield already exists for this
// workflow+step+iteration (idempotency), it returns the existing one.
// If the existing yield is already resolved, AlreadyResolved=true is returned
// so the workflow can skip signal waiting.
func (a *YieldCreateActivity) Execute(ctx context.Context, input YieldCreateInput) (YieldCreateOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[YieldCreate] Creating yield record",
		"chatID", input.ChatID,
		"workflowID", input.WorkflowID,
		"stepID", input.StepID,
		"loopIteration", input.LoopIteration)

	// IDEMPOTENCY: Check if we already created a yield for this workflow+step+iteration.
	// When ToolCallID is set (ask_user), we also match on it to disambiguate multiple
	// ask_user yields within the same loop iteration.
	existingYields, err := a.repo.GetYieldsByWorkflowStepIteration(ctx, input.WorkflowID, input.StepID, input.LoopIteration)
	if err == nil && len(existingYields) > 0 {
		// Find the matching yield — filter by tool_call_id if provided
		var match *db.Yield
		if input.ToolCallID != "" {
			for _, y := range existingYields {
				if y.Metadata != nil && containsToolCallID(*y.Metadata, input.ToolCallID) {
					match = y
					break
				}
			}
		} else {
			match = existingYields[0]
		}

		if match != nil {
			logger.Info("[YieldCreate] Found existing yield",
				"yieldID", match.ID,
				"status", match.Status)

			if match.Status == db.YieldStatusResolved {
				actionTaken := ""
				if match.ActionTaken != nil {
					actionTaken = *match.ActionTaken
				}
				return YieldCreateOutput{
					YieldID:         match.ID,
					AlreadyResolved: true,
					ActionTaken:     actionTaken,
				}, nil
			}

			// Still pending — return yield ID so workflow waits on signal
			return YieldCreateOutput{
				YieldID:         match.ID,
				AlreadyResolved: false,
			}, nil
		}
	}

	// Generate yield ID and create record
	yieldID := uuid.New().String()

	var loopNodeID *string
	if input.LoopNodeID != "" {
		loopNodeID = &input.LoopNodeID
	}
	loopIteration := input.LoopIteration

	// Resolve temporal workflow ID: use provided value, fall back to WorkflowID (root workflows)
	temporalWorkflowID := input.TemporalWorkflowID
	if temporalWorkflowID == "" {
		temporalWorkflowID = input.WorkflowID
	}

	yield := &db.Yield{
		ID:                 yieldID,
		ChatID:             input.ChatID,
		WorkflowID:         input.WorkflowID,
		TemporalWorkflowID: temporalWorkflowID,
		ThreadID:           input.ThreadID,
		StepID:             input.StepID,
		LoopNodeID:         loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.YieldStatusPending,
		Metadata:           input.Metadata,
		CreatedAt:          time.Now().UTC(),
	}

	// Create yield record and emit chat_update atomically
	err = a.repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := a.repo.CreateYield(txCtx, yield); err != nil {
			return fmt.Errorf("failed to create yield: %w", err)
		}

		// Mark chat as unread so UI shows notification
		if err := a.repo.UpdateChatUnread(txCtx, input.ChatID, true, "yield_pending"); err != nil {
			logger.Warn("[YieldCreate] Failed to update chat unread",
				"error", err,
				"chatID", input.ChatID)
		}

		// Emit yield chat_update for frontend
		yieldUpdate := db.YieldUpdate{
			YieldID:    yieldID,
			ChatID:     input.ChatID,
			WorkflowID: input.WorkflowID,
			StepID:     input.StepID,
			Status:     "pending",
		}
		if input.Metadata != nil {
			yieldUpdate.Metadata = *input.Metadata
		}
		if err := a.repo.EmitYieldUpdate(txCtx, input.ChatID, yieldUpdate); err != nil {
			return fmt.Errorf("failed to emit yield update: %w", err)
		}

		return nil
	})
	if err != nil {
		return YieldCreateOutput{}, err
	}

	logger.Info("[YieldCreate] Yield created successfully",
		"yieldID", yieldID,
		"chatID", input.ChatID)

	return YieldCreateOutput{
		YieldID:         yieldID,
		AlreadyResolved: false,
	}, nil
}

// ============================================================================
// ACTIVITY: YieldResolveActivity
// ============================================================================

// YieldResolveActivity resolves a yield record in the DB. Used by the workflow
// when the signal timer expires (timeout case).
type YieldResolveActivity struct {
	repo db.Repository
}

// NewYieldResolveActivity creates a new YieldResolveActivity
func NewYieldResolveActivity(repo db.Repository) *YieldResolveActivity {
	return &YieldResolveActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *YieldResolveActivity) Name() string {
	return "YieldResolve"
}

// DisplayName returns human-readable name for UI
func (a *YieldResolveActivity) DisplayName() string {
	return "YieldResolve"
}

// Description returns what the activity does
func (a *YieldResolveActivity) Description() string {
	return "Resolve a yield record in the database"
}

// Category returns the activity category for UI grouping
func (a *YieldResolveActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute resolves the yield record.
func (a *YieldResolveActivity) Execute(ctx context.Context, input YieldResolveInput) (YieldResolveOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[YieldResolve] Resolving yield",
		"yieldID", input.YieldID,
		"action", input.Action)

	if err := a.repo.ResolveYield(ctx, input.YieldID, input.Action); err != nil {
		return YieldResolveOutput{}, fmt.Errorf("failed to resolve yield: %w", err)
	}

	return YieldResolveOutput{Success: true}, nil
}

// containsToolCallID checks if a yield's metadata JSON contains the given tool_call_id.
func containsToolCallID(metadata, toolCallID string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(metadata), &m); err != nil {
		return false
	}
	id, _ := m["tool_call_id"].(string)
	return id == toolCallID
}
