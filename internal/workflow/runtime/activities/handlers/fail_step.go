// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

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

// Execute always returns an error with the provided message
func (a *FailStepActivity) Execute(ctx context.Context, input FailStepInput) (FailStepOutput, error) {
	logging.Warn("[FailStep] Workflow validation error", "error", input.Error)
	return FailStepOutput{}, fmt.Errorf("workflow validation failed: %s", input.Error)
}
