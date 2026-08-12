// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
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

// wireStatusFromVerb maps a lifecycle verb to the status string the UI's
// thread records use. The UI treats a thread as active on "running"/"active"
// only (useIsThreadActive in web/src/store/threadActivityStore.ts), so
// emitting the raw verb "started" read as NOT-active: a spawned sub-thread
// showed no thinking indicator for its whole run, because "started" arrives
// last and overwrites the "running" record the spawn path had already
// written. Non-terminal verbs are normalised to "running"; terminal ones pass
// through unchanged, since the UI compares those by name.
func wireStatusFromVerb(verb string) string {
	switch verb {
	case "completed", "failed", "cancelled", "expired":
		return verb
	default:
		return "running"
	}
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

	if terminal {
		a.resolveMailbox(ctx, input.ThreadID)
	}

	if err := a.emitThreadUpdate(ctx, input, completedAt); err != nil {
		return ThreadStatusOutput{}, err
	}

	return ThreadStatusOutput{Success: true}, nil
}

// resolveMailbox marks a terminated thread's still-queued mailbox rows
// undelivered, so a message that arrived too late to be drained says so
// instead of sitting at "queued" forever.
//
// This is the exit point for EVERY terminal path — normal completion,
// failure, cancellation, expiry — because every one of them reaches a
// terminal threads.status through this single activity, and
// threadStatusFromVerb has already decided which verbs those are. Handling
// them here rather than at the loop's break statements is what makes the
// coverage total instead of per-path.
//
// It lives in the ACTIVITY, not in workflow code, deliberately. The drain at
// the loop-step boundary needs workflow.GetVersion (changeID
// "agent-mailbox-drain") because it emits a command that replayed histories
// have no record of; a second workflow-side call site would need its own
// distinct changeID or in-flight runs would wedge with TMPRL1100. An activity
// adds no workflow command at all, so this resolution is invisible to replay
// and needs no gate — and since ThreadStatus is already invoked on every
// terminal transition, it costs no additional activity dispatch either.
//
// Best-effort by the same rule as the drain itself: the thread's lifecycle
// write has already landed and is the more important fact. Failing the
// activity here would retry the whole lifecycle write to fix mailbox
// bookkeeping, and the reconciler's own sweep is the durable backstop that
// picks up anything missed.
func (a *ThreadStatusActivity) resolveMailbox(ctx context.Context, threadID string) {
	resolved, err := a.repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, threadID)
	if err != nil {
		logging.Warn("[ThreadStatus] Failed to resolve mailbox for terminated thread",
			"threadID", threadID,
			"error", err)
		return
	}
	if resolved > 0 {
		// ERROR, not Info: every row here is a message a human or a peer
		// agent sent that will never be read. It is rare, it is a real loss
		// of intent, and it is the signal that says how often this race
		// actually fires.
		logging.Error("[ThreadStatus] Thread exited with undelivered mailbox messages — queued after its last loop boundary",
			"threadID", threadID,
			"rows", resolved)
	}
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
		"status":      wireStatusFromVerb(input.Status),
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
