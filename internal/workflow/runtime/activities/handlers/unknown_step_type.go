// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// UnknownStepTypeInput is the input for UnknownStepType activity
type UnknownStepTypeInput struct {
	StepID   string                 `json:"step_id"`
	StepType string                 `json:"step_type,omitempty"`
	Inputs   map[string]interface{} `json:"inputs,omitempty"`
}

// UnknownStepTypeOutput is the output for UnknownStepType activity
type UnknownStepTypeOutput struct {
	Error string `json:"error"`
}

// UnknownStepTypeActivity handles unknown step types
type UnknownStepTypeActivity struct{}

// NewUnknownStepTypeActivity creates a new UnknownStepType activity
func NewUnknownStepTypeActivity() *UnknownStepTypeActivity {
	return &UnknownStepTypeActivity{}
}

// Name returns the activity name
func (a *UnknownStepTypeActivity) Name() string {
	return "UnknownStepType"
}

// DisplayName returns human-readable name for UI
func (a *UnknownStepTypeActivity) DisplayName() string {
	return "Unknown Step Type"
}

// Description returns what the activity does
func (a *UnknownStepTypeActivity) Description() string {
	return "Placeholder for unrecognized step types (will fail)"
}

// Category returns the activity category for UI grouping
func (a *UnknownStepTypeActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

// Execute returns an error indicating the step type is not supported
func (a *UnknownStepTypeActivity) Execute(ctx context.Context, input UnknownStepTypeInput) (UnknownStepTypeOutput, error) {
	errorMsg := fmt.Sprintf("unknown step type for step %s", input.StepID)
	if input.StepType != "" {
		errorMsg = fmt.Sprintf("unknown step type '%s' for step %s", input.StepType, input.StepID)
	}

	return UnknownStepTypeOutput{
		Error: errorMsg,
	}, fmt.Errorf("%s", errorMsg)
}
