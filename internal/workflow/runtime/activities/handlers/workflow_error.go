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
	// Thread the error belongs to. Without it an error is chat-scoped, and the
	// timeline has nothing to filter on — so a single "Paused: no machine is
	// connected" from the main thread rendered inside EVERY thread of the chat,
	// including spawned threads that started hours after the incident and
	// recovered. An error is an event on one thread, not a property of the
	// whole conversation.
	Thread string `json:"thread,omitempty"`
	// ErrorID, when set, IS the chat_update entity id this error is written
	// under, replacing the uuid that would otherwise be minted here.
	//
	// It exists so a retry-exhaustion error can land on the SAME row the
	// failing activity's own retry series already occupies. The activity
	// wrapper keys every attempt of one activity under
	// activity-error-<workflowID>-<activityID> (see runtime.activityErrorEventID),
	// and both the frontend's dedup-by-id and the server's
	// GetLatestNonMessageUpdatesPerEntity collapse per entity id — so a fresh
	// uuid here could not dedup against the series it duplicates, and the final
	// attempt rendered TWICE: once from the wrapper, once from the exhaustion
	// handler, 19ms apart, describing one failure.
	//
	// Leave it empty when there is no activity to key on. The reconciler does
	// exactly that: a hard Temporal termination has no failing activity, so its
	// error is its own event and a minted uuid is the correct id.
	ErrorID string `json:"error_id,omitempty"`
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
	errorID, err := WriteWorkflowError(ctx, a.repo, input)
	if err != nil {
		return WorkflowErrorOutput{Success: false}, err
	}
	return WorkflowErrorOutput{Success: true, ErrorID: errorID}, nil
}

// WriteWorkflowError writes one workflow-level error to chat_updates and
// returns the id it was written under.
//
// This is the payload shape the frontend's ErrorUpdate interface reads
// (web/src/types/streaming.ts), and it lives here — outside the activity — so
// that callers who are NOT running inside a workflow can emit the same error
// the workflow would have. The reconciler is the motivating one: on a hard
// Temporal termination the worker never receives another workflow task, so no
// workflow code (and therefore no activity) ever runs again, and the
// reconciler is the only component left that observes the death. It has a
// db.Repository but no activity context, so it calls this directly.
//
// Duplicating the payload in that caller instead would mean the two error
// shapes drift the first time a field is added, and the UI would silently
// render one of them wrong.
func WriteWorkflowError(ctx context.Context, repo db.Repository, input WorkflowErrorInput) (string, error) {
	// An explicit id means this error REPLACES an existing row rather than
	// adding one — see WorkflowErrorInput.ErrorID. Absent it, the error is its
	// own event and gets its own id.
	errorID := input.ErrorID
	if errorID == "" {
		errorID = uuid.New().String()
	}

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

	// Thread scoping. Omitted when unknown rather than defaulted to the chat id:
	// a reader must be able to tell "this error belongs to the main thread" from
	// "nobody said", and silently claiming the former is how a main-thread error
	// ends up rendered in every spawned thread.
	if input.Thread != "" {
		errorData["thread"] = input.Thread
	}

	// Include a clean summary if available
	if input.ErrorSummary != "" {
		errorData["error_summary"] = input.ErrorSummary
	}

	// Marshal update data
	errorDataJSON, err := json.Marshal(errorData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal workflow error: %w", err)
	}

	// Write to chat_updates (for per-chat websocket)
	if err := repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeError, errorID, string(errorDataJSON)); err != nil {
		return "", fmt.Errorf("failed to create chat_update: %w", err)
	}

	return errorID, nil
}
