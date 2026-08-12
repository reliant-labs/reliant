// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES
// ============================================================================

// EmitToolCallStatusInput is the input for EmitToolCallStatus activity.
// ToolCallID is the LLM tool-call id — the canonical key the UI looks status
// up by. See db.ToolCallUpdate for why no content-block id belongs here.
type EmitToolCallStatusInput struct {
	ChatID     string `json:"chat_id" reliant:"-"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Status     string `json:"status"`
	// ChildWorkflowID is set for spawn calls. Recording it on the row makes a
	// spawn's completion a join from the tool call to its child workflow
	// instead of matching on strings after the fact.
	ChildWorkflowID string `json:"child_workflow_id,omitempty"`
	// Input is the spawn tool's raw JSON arguments, persisted alongside the
	// status so a reloaded spawn card can show what was requested.
	Input string `json:"input,omitempty"`
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
		ToolCallID: input.ToolCallID,
		ToolName:   input.ToolName,
		Status:     db.ToolCallStatus(input.Status),
		Timestamp:  time.Now().Format(time.RFC3339),
	}

	// Persist before emitting so a durable row exists even if the event fails.
	// The two are complements: the event drives live streaming, the row
	// survives a reload.
	a.persist(ctx, input)

	if err := a.repo.EmitToolCallUpdate(ctx, input.ChatID, update); err != nil {
		logging.Error("[EmitToolCallStatus] Failed", "error", err, "tool_call_id", input.ToolCallID, "status", input.Status)
		return EmitToolCallStatusOutput{Success: false}, nil // best-effort, don't fail
	}

	return EmitToolCallStatusOutput{Success: true}, nil
}

// persist writes the durable tool_calls row for this status transition.
//
// This activity is the ONLY way workflow code can record tool status: the
// spawn path lives in Temporal workflow code, which must stay deterministic
// and therefore cannot touch the repository directly. Routing the write
// through the activity keeps the workflow deterministic while still making
// spawn status durable.
//
// Best-effort by the same rule as the event emission above: a failed write is
// logged, never returned, and never fails a spawn.
func (a *EmitToolCallStatusActivity) persist(ctx context.Context, input EmitToolCallStatusInput) {
	if input.ChatID == "" || input.ToolCallID == "" {
		return
	}

	status := toolCallStatusFromString(input.Status)
	if status == core.ToolCallStatusUnspecified {
		return
	}

	now := time.Now()
	call := &core.ToolCall{
		ID:          input.ToolCallID,
		ChatID:      input.ChatID,
		ToolName:    input.ToolName,
		Input:       toolInputToJSON(input.Input),
		Status:      status,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if input.ChildWorkflowID != "" {
		call.ChildWorkflowID = &input.ChildWorkflowID
	}
	switch status {
	case core.ToolCallStatusExecuting:
		call.StartedAt = &now
	case core.ToolCallStatusCompleted, core.ToolCallStatusFailed, core.ToolCallStatusCancelled:
		call.CompletedAt = &now
	}

	if err := db.UpsertToolCallStatus(ctx, a.repo, call); err != nil {
		logging.Error("[EmitToolCallStatus] Failed to persist tool call",
			"error", err, "tool_call_id", input.ToolCallID, "status", input.Status)
	}
}

// toolCallStatusFromString maps the wire status string used by chat_updates
// onto the durable status enum. An unrecognized string yields Unspecified,
// which persist() treats as "don't write" rather than storing a zero status
// no reader can interpret.
func toolCallStatusFromString(status string) core.ToolCallStatus {
	switch db.ToolCallStatus(status) {
	case db.ToolCallStatusPending:
		return core.ToolCallStatusPending
	case db.ToolCallStatusExecuting:
		return core.ToolCallStatusExecuting
	case db.ToolCallStatusCompleted:
		return core.ToolCallStatusCompleted
	case db.ToolCallStatusFailed:
		return core.ToolCallStatusFailed
	case db.ToolCallStatusCancelled:
		return core.ToolCallStatusCancelled
	case db.ToolCallStatusBackgrounded:
		return core.ToolCallStatusBackgrounded
	default:
		return core.ToolCallStatusUnspecified
	}
}
