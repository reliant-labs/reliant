// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES
// ============================================================================

// EmitToolCallStatusInput is the input for EmitToolCallStatus activity
type EmitToolCallStatusInput struct {
	ChatID         string `json:"chat_id" reliant:"-"`
	ContentBlockID string `json:"content_block_id"`
	ToolCallID     string `json:"tool_call_id"`
	ToolName       string `json:"tool_name"`
	Status         string `json:"status"`
}

// EmitToolCallStatusOutput is the output from EmitToolCallStatus activity
type EmitToolCallStatusOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// EmitToolCallStatusActivity emits tool call status updates to chat_updates.
// Used by spawn inline workflows to report per-tool-call status independently
// of the regular ExecuteTools activity batch.
type EmitToolCallStatusActivity struct {
	repo db.Repository
}

func NewEmitToolCallStatusActivity(repo db.Repository) *EmitToolCallStatusActivity {
	return &EmitToolCallStatusActivity{repo: repo}
}

func (a *EmitToolCallStatusActivity) Name() string        { return "EmitToolCallStatus" }
func (a *EmitToolCallStatusActivity) DisplayName() string { return "Emit Tool Call Status" }
func (a *EmitToolCallStatusActivity) Description() string { return "Emits a tool call status update" }
func (a *EmitToolCallStatusActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

func (a *EmitToolCallStatusActivity) Execute(ctx context.Context, input EmitToolCallStatusInput) (EmitToolCallStatusOutput, error) {
	update := db.ToolCallUpdate{
		ContentBlockID: input.ContentBlockID,
		ToolCallID:     input.ToolCallID,
		ToolName:       input.ToolName,
		Status:         db.ToolCallStatus(input.Status),
		Timestamp:      time.Now().Format(time.RFC3339),
	}

	if err := a.repo.EmitToolCallUpdate(ctx, input.ChatID, update); err != nil {
		logging.Error("[EmitToolCallStatus] Failed", "error", err, "tool_call_id", input.ToolCallID, "status", input.Status)
		return EmitToolCallStatusOutput{Success: false}, nil // best-effort, don't fail
	}

	return EmitToolCallStatusOutput{Success: true}, nil
}
