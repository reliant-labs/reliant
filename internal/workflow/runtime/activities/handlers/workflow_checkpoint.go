// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// WorkflowCheckpointInput is the input for the WorkflowCheckpoint activity.
// It records the position a workflow run has reached: the top-level node it
// just entered and, for loop nodes, the loop iteration in flight. This is the
// position truth used by SendMessage to resume an interrupted
// (failed/terminated) run at position instead of restarting at graph entry.
type WorkflowCheckpointInput struct {
	ChatID        string `json:"chat_id" reliant:"-"`
	WorkflowID    string `json:"workflow_id"`
	NodeID        string `json:"node_id"`
	LoopIteration int64  `json:"loop_iteration,omitempty"`
}

// WorkflowCheckpointOutput is the output from the WorkflowCheckpoint activity.
type WorkflowCheckpointOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// WorkflowCheckpointActivity persists workflow position checkpoints. It is a
// lifecycle activity (like WorkflowStatus): pure bookkeeping that must not
// flip the workflow's running state.
type WorkflowCheckpointActivity struct {
	repo db.Repository
}

// NewWorkflowCheckpointActivity creates a new WorkflowCheckpointActivity.
func NewWorkflowCheckpointActivity(repo db.Repository) *WorkflowCheckpointActivity {
	return &WorkflowCheckpointActivity{repo: repo}
}

// Name returns the activity name for registration.
func (a *WorkflowCheckpointActivity) Name() string {
	return "WorkflowCheckpoint"
}

// Execute upserts the position checkpoint for the workflow ID.
func (a *WorkflowCheckpointActivity) Execute(ctx context.Context, input WorkflowCheckpointInput) (WorkflowCheckpointOutput, error) {
	if err := a.repo.UpsertWorkflowCheckpoint(ctx, &db.WorkflowCheckpoint{
		WorkflowID:    input.WorkflowID,
		ChatID:        input.ChatID,
		NodeID:        input.NodeID,
		LoopIteration: input.LoopIteration,
	}); err != nil {
		return WorkflowCheckpointOutput{Success: false}, err
	}
	return WorkflowCheckpointOutput{Success: true}, nil
}
