// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// WorkflowCheckpointInput is defined in activities/types: this activity runs
// as a LOCAL activity (runtime.executeBookkeeping), whose input must already be
// the registered parameter type, so the workflow names it too.
type WorkflowCheckpointInput = types.WorkflowCheckpointInput

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
	repo WorkflowCheckpointStore
}

// NewWorkflowCheckpointActivity creates a new WorkflowCheckpointActivity.
func NewWorkflowCheckpointActivity(repo WorkflowCheckpointStore) *WorkflowCheckpointActivity {
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
		CatalogPin:    input.CatalogPin,
	}); err != nil {
		return WorkflowCheckpointOutput{Success: false}, err
	}
	return WorkflowCheckpointOutput{Success: true}, nil
}
