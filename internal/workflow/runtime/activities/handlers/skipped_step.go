// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// SkippedStepInput is the input for SkippedStep activity
type SkippedStepInput struct {
	WorkflowID string `json:"workflow_id"`
	ChatID     string `json:"chat_id" reliant:"-"`
	StepID     string `json:"step_id"`
	// NodePath is the node's fully-qualified dotted graph position. Carried for
	// observability alongside the bare StepID; not persisted.
	NodePath  string `json:"node_path,omitempty"`
	Condition string `json:"condition"` // The condition that evaluated to false
}

// SkippedStepOutput is the output from SkippedStep activity
type SkippedStepOutput struct {
	Skipped bool `json:"skipped"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// SkippedStepActivity implements TypedActivity[SkippedStepInput, SkippedStepOutput]
// This is a lightweight activity that records when a node is skipped due to its condition.
// The ActivityWrapper will automatically record it to step_executions for UI visibility.
type SkippedStepActivity struct{}

// NewSkippedStepActivity creates a new SkippedStepActivity
func NewSkippedStepActivity() *SkippedStepActivity {
	return &SkippedStepActivity{}
}

// Name returns the activity name for registration
func (a *SkippedStepActivity) Name() string {
	return model.ActivitySkippedStep
}

// DisplayName returns human-readable name for UI
func (a *SkippedStepActivity) DisplayName() string {
	return "Skipped Step"
}

// Description returns what the activity does
func (a *SkippedStepActivity) Description() string {
	return "Records when a node is skipped due to its condition evaluating to false"
}

// Category returns the activity category for UI grouping
func (a *SkippedStepActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

// Execute is a no-op - the ActivityWrapper handles recording to step_executions
func (a *SkippedStepActivity) Execute(ctx context.Context, input SkippedStepInput) (SkippedStepOutput, error) {
	// This activity does nothing - it exists solely so the ActivityWrapper
	// records it to step_executions, making the skip visible in the UI.
	return SkippedStepOutput{Skipped: true}, nil
}
