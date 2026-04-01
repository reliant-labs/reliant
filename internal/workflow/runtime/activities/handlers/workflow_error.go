// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// WorkflowErrorInput is the input for WorkflowError activity
type WorkflowErrorInput struct {
	ChatID       string `json:"chat_id" reliant:"-"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	ErrorMessage string `json:"error_message"`
	ErrorType    string `json:"error_type"`              // e.g., "workflow_parse_error", "template_error"
	ErrorSummary string `json:"error_summary,omitempty"` // Clean, user-friendly summary (extracted from nested errors)
}

// WorkflowErrorOutput is the output from WorkflowError activity
type WorkflowErrorOutput struct {
	Success bool   `json:"success"`
	ErrorID string `json:"error_id"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// WorkflowErrorActivity writes workflow-level errors to chat_updates for UI notifications
type WorkflowErrorActivity struct {
	repo db.Repository
}

// NewWorkflowErrorActivity creates a new WorkflowErrorActivity
func NewWorkflowErrorActivity(repo db.Repository) *WorkflowErrorActivity {
	return &WorkflowErrorActivity{
		repo: repo,
	}
}

// Name returns the activity name for registration
func (a *WorkflowErrorActivity) Name() string {
	return "WorkflowError"
}

// Execute writes a workflow error to chat_updates table
func (a *WorkflowErrorActivity) Execute(ctx context.Context, input WorkflowErrorInput) (WorkflowErrorOutput, error) {
	// Generate unique error ID
	errorID := uuid.New().String()

	// Build error data matching the ErrorUpdate interface expected by frontend
	// See web/src/types/streaming.ts ErrorUpdate interface
	errorData := map[string]interface{}{
		"update_type":   "error",
		"id":            errorID,
		"chat_id":       input.ChatID,
		"activity_type": input.ErrorType, // Maps to activity_type field in ErrorUpdate
		"activity_id":   input.WorkflowID,
		"error_message": input.ErrorMessage,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"workflow_id":   input.WorkflowID,
	}

	// Include a clean summary if available
	if input.ErrorSummary != "" {
		errorData["error_summary"] = input.ErrorSummary
	}

	// Marshal update data
	errorDataJSON, err := json.Marshal(errorData)
	if err != nil {
		return WorkflowErrorOutput{Success: false}, fmt.Errorf("failed to marshal workflow error: %w", err)
	}

	// Write to chat_updates (for per-chat websocket)
	if err := a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeError, errorID, string(errorDataJSON)); err != nil {
		return WorkflowErrorOutput{Success: false}, fmt.Errorf("failed to create chat_update: %w", err)
	}

	return WorkflowErrorOutput{Success: true, ErrorID: errorID}, nil
}
