// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// ThreadStatusInput is the input for the ThreadStatus activity.
//
// This replaces the "thread:<node>" workflow records that used to stand in for
// thread lifecycle. Those were rows in `workflows` that were not workflow
// executions, distinguished only by a name prefix — every consumer had to know
// to filter them out, and the one that forgot rendered spawned threads as
// inline workflow nodes. Threads own their lifecycle now.
type ThreadStatusInput struct {
	ChatID   string `json:"chat_id" reliant:"-"`
	ThreadID string `json:"thread_id"`
	// Status is a lifecycle verb — "started", "completed", "failed",
	// "cancelled" — mapped to db.ThreadStatus* on write.
	Status string `json:"status"`
	// WorkflowID is the workflow that owns the thread. Used only for the UI
	// stream, which keys thread updates by workflow ID.
	WorkflowID string `json:"workflow_id,omitempty"`
	// NodeID is the graph node that owns the thread, for logging and for the
	// stream's display name.
	NodeID string `json:"node_id,omitempty"`
	// ThreadTitle is the human-readable thread name.
	ThreadTitle string `json:"thread_title,omitempty"`
	// Origin is how the thread came to exist (db.ThreadOrigin*).
	Origin string `json:"origin,omitempty"`
	// RouterDecision carries routing metadata when a router node created the
	// thread.
	RouterDecision *RouterDecisionInfo `json:"router_decision,omitempty"`
}

// ThreadStatusOutput is the output from the ThreadStatus activity.
type ThreadStatusOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// ThreadStatusActivity records thread lifecycle on the threads table and
// mirrors it to chat_updates for the UI.
type ThreadStatusActivity struct {
	repo db.Repository
}

// NewThreadStatusActivity creates a new ThreadStatusActivity.
func NewThreadStatusActivity(repo db.Repository) *ThreadStatusActivity {
	return &ThreadStatusActivity{repo: repo}
}

// Name returns the activity name for registration.
func (a *ThreadStatusActivity) Name() string {
	return "ThreadStatus"
}

// threadStatusFromVerb maps a lifecycle verb to a stored status. Terminal
// statuses mirror CHAT_WORKFLOW_STATUS so a thread's state is directly
// comparable to its workflow's.
func threadStatusFromVerb(verb string) (int32, bool) {
	switch verb {
	case "started":
		return db.ThreadStatusRunning, false
	case "completed":
		return db.ThreadStatusCompleted, true
	case "failed":
		return db.ThreadStatusFailed, true
	case "cancelled":
		return db.ThreadStatusCancelled, true
	case "expired":
		return db.ThreadStatusExpired, true
	default:
		return db.ThreadStatusRunning, false
	}
}

// Execute records the thread's lifecycle state and emits a UI update.
func (a *ThreadStatusActivity) Execute(ctx context.Context, input ThreadStatusInput) (ThreadStatusOutput, error) {
	if input.ThreadID == "" {
		return ThreadStatusOutput{}, fmt.Errorf("thread_id is required")
	}

	status, terminal := threadStatusFromVerb(input.Status)

	var completedAt *time.Time
	if terminal {
		now := time.Now().UTC()
		completedAt = &now
	}

	if _, err := a.repo.UpdateThreadStatus(ctx, input.ThreadID, status, completedAt); err != nil {
		return ThreadStatusOutput{}, fmt.Errorf("failed to update thread status: %w", err)
	}

	if err := a.emitThreadUpdate(ctx, input, completedAt); err != nil {
		return ThreadStatusOutput{}, err
	}

	return ThreadStatusOutput{Success: true}, nil
}

// emitThreadUpdate mirrors the lifecycle change onto chat_updates. The shape
// matches ActiveThreadUpdate in web/src/types/streaming.ts.
func (a *ThreadStatusActivity) emitThreadUpdate(ctx context.Context, input ThreadStatusInput, completedAt *time.Time) error {
	// The stream keys thread records by workflow ID; fall back to the thread
	// ID so an update is never dropped for want of a key.
	id := input.WorkflowID
	if id == "" {
		id = input.ThreadID
	}

	updateData := map[string]interface{}{
		"update_type": "thread",
		"id":          id,
		"chat_id":     input.ChatID,
		"thread":      input.ThreadID,
		"workflow_id": input.WorkflowID,
		"status":      input.Status,
	}
	if input.ThreadTitle != "" {
		updateData["thread_title"] = input.ThreadTitle
	}
	if input.Origin != "" {
		updateData["origin"] = input.Origin
	}
	if input.NodeID != "" {
		updateData["origin_node_id"] = input.NodeID
	}
	if completedAt != nil {
		updateData["completed_at"] = completedAt.Format(time.RFC3339)
	}
	if input.RouterDecision != nil {
		updateData["router_decision"] = map[string]string{
			"workflow": input.RouterDecision.Workflow,
			"preset":   input.RouterDecision.Preset,
		}
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal thread update: %w", err)
	}

	return a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeThread, id, string(updateDataJSON))
}
