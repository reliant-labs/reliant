// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// DebugTraceInput contains checkpoint information for debugging workflow execution
type DebugTraceInput struct {
	ChatID        string                 `json:"chat_id" reliant:"-"`
	WorkflowID    string                 `json:"workflow_id"`
	StepID        string                 `json:"step_id"`
	LoopNodeID    string                 `json:"loop_node_id,omitempty"`
	LoopIteration int                    `json:"loop_iteration"`
	Checkpoint    string                 `json:"checkpoint"`
	Data          map[string]interface{} `json:"data"`
}

// DebugTraceOutput is empty - this is fire-and-forget
type DebugTraceOutput struct{}

// DebugTraceActivity writes debug checkpoints to chat_updates for workflow debugging
type DebugTraceActivity struct {
	repo db.Repository
}

// NewDebugTraceActivity creates a new debug trace activity
func NewDebugTraceActivity(repo db.Repository) *DebugTraceActivity {
	return &DebugTraceActivity{repo: repo}
}

// Name returns the activity name
func (a *DebugTraceActivity) Name() string {
	return "DebugTrace"
}

// DisplayName returns human-readable name for UI
func (a *DebugTraceActivity) DisplayName() string {
	return "Debug Trace"
}

// Description returns what the activity does
func (a *DebugTraceActivity) Description() string {
	return "Write debug checkpoints for workflow execution tracing"
}

// Category returns the activity category for UI grouping
func (a *DebugTraceActivity) Category() schema.ActivityCategory {
	return schema.CategoryDebug
}

// Execute writes a debug checkpoint to chat_updates
func (a *DebugTraceActivity) Execute(ctx context.Context, input DebugTraceInput) (DebugTraceOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[DebugTrace] Checkpoint",
		"checkpoint", input.Checkpoint,
		"workflowID", input.WorkflowID,
		"data", input.Data,
	)

	// Build update data
	updateData := map[string]interface{}{
		"checkpoint":  input.Checkpoint,
		"workflow_id": input.WorkflowID,
		"data":        input.Data,
		"timestamp":   time.Now().UnixMilli(),
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		logger.Warn("[DebugTrace] Failed to marshal checkpoint data", "error", err)
		return DebugTraceOutput{}, nil // Don't fail on debug issues
	}

	// Write to chat_updates with type "execution_log"
	entityID := fmt.Sprintf("debug-%s-%d", input.Checkpoint, time.Now().UnixNano())
	if err := a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeExecutionLog, entityID, string(updateDataJSON)); err != nil {
		logger.Warn("[DebugTrace] Failed to write checkpoint", "error", err)
		return DebugTraceOutput{}, nil // Don't fail on debug issues
	}

	return DebugTraceOutput{}, nil
}
