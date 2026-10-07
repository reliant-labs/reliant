// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// FailStepInput is the input for FailStep activity
type FailStepInput struct {
	ChatID string `json:"chat_id" reliant:"-"` // Required for error event to be written to chat_updates
	Error  string `json:"error"`

	// Where the failing node is. The handler never reads these; the activity
	// wrapper does, to file the failure under that node — a failed node event
	// and a step row carrying the error — so the canvas can say which step
	// broke. They must be fields: Temporal decodes the input into this type,
	// and a key it has no field for never reaches the wrapper.
	WorkflowID    string `json:"workflow_id,omitempty" reliant:"-"`
	StepID        string `json:"step_id,omitempty" reliant:"-"`
	LoopNodeID    string `json:"loop_node_id,omitempty" reliant:"-"`
	LoopIteration int    `json:"loop_iteration,omitempty" reliant:"-"`
	NodePath      string `json:"node_path,omitempty" reliant:"-"`
}

// FailStepOutput is the output from FailStep activity (always fails)
type FailStepOutput struct{}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// FailStepActivity is a special activity that always fails with a given error message.
// Used for validation errors detected at runtime that should fail the workflow.
type FailStepActivity struct{}

// NewFailStepActivity creates a new FailStepActivity
func NewFailStepActivity() *FailStepActivity {
	return &FailStepActivity{}
}

// Name returns the activity name for registration
func (a *FailStepActivity) Name() string {
	return "FailStep"
}

// DisplayName returns human-readable name for UI
func (a *FailStepActivity) DisplayName() string {
	return "Fail Step"
}

// Description returns what the activity does
func (a *FailStepActivity) Description() string {
	return "Intentionally fail the workflow with a custom error message"
}

// Category returns the activity category for UI grouping
func (a *FailStepActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

// Execute always returns an error with the provided message.
//
// The error is NON-RETRYABLE. What FailStep reports — a {{ }} expression
// that could not be evaluated, a node config that cannot run — is decided by
// the workflow's definition and inputs, so every retry fails the same way.
// Retrying used to keep such a run alive indefinitely under Temporal's default
// policy (unlimited attempts, backing off to minutes apart): a test run with
// one broken expression never ended, and never said it had failed.
func (a *FailStepActivity) Execute(ctx context.Context, input FailStepInput) (FailStepOutput, error) {
	logging.Warn("[FailStep] Workflow validation error", "error", input.Error)
	return FailStepOutput{}, temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("workflow validation failed: %s", input.Error), "", nil)
}
