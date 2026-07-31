// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ============================================================================
// DELTA IDENTITY: message id pre-allocation + stream_finalized markers
// ============================================================================
//
// The workflow mints the assistant message id BEFORE dispatching CallLLM (via
// SideEffect, so retries re-stream under the same id), stamps it into
// RuntimeContext.AssistantMessageID, and guarantees the core invariant:
// every allocated id is eventually finalized with exactly one
// stream_finalized chat update (completed / aborted / cancelled).
//
// REPLAY DISCIPLINE: all of this is gated on
// workflow.GetVersion("preallocated-message-id"). Histories recorded before
// this change replay with DefaultVersion — no SideEffect, no id, and (because
// every finalize hook is keyed on a non-empty id) no finalize commands. New
// executions get version 1 and the full protocol.

// streamFinalizedVersionGate is the GetVersion changeID shared by every code
// path that allocates ids or emits finalize markers.
const streamFinalizedVersionGate = "preallocated-message-id"

// Terminal reasons for a message stream (mirrors db.StreamFinalizedReason;
// string literals used to avoid importing db into workflow code).
const (
	streamReasonCompleted = "completed"
	streamReasonAborted   = "aborted"
	streamReasonCancelled = "cancelled"
)

// streamIDTrackerKey is the workflow-context key for the StreamIDTracker.
type streamIDTrackerKey struct{}

// outstandingStream records where an allocated-but-not-yet-finalized message
// id belongs, so workflow-completion cleanup can emit its aborted marker.
type outstandingStream struct {
	ChatID string
	Thread string
}

// StreamIDTracker tracks allocated assistant message ids that have not yet
// been finalized. Lives in the workflow context (workflow.WithValue) so every
// executor — step, inline, loop, router — sees the same instance without
// plumbing. Workflow code is cooperatively scheduled, so no locking.
type StreamIDTracker struct {
	outstanding map[string]outstandingStream
}

// NewStreamIDTracker creates an empty tracker.
func NewStreamIDTracker() *StreamIDTracker {
	return &StreamIDTracker{outstanding: make(map[string]outstandingStream)}
}

// WithStreamIDTracker stores the tracker in the workflow context.
func WithStreamIDTracker(ctx workflow.Context, t *StreamIDTracker) workflow.Context {
	return workflow.WithValue(ctx, streamIDTrackerKey{}, t)
}

// streamIDTrackerFrom retrieves the tracker, or nil (tests / legacy paths).
func streamIDTrackerFrom(ctx workflow.Context) *StreamIDTracker {
	t, _ := ctx.Value(streamIDTrackerKey{}).(*StreamIDTracker)
	return t
}

// Register records an allocated id as outstanding.
func (t *StreamIDTracker) Register(id, chatID, thread string) {
	if t == nil || id == "" {
		return
	}
	t.outstanding[id] = outstandingStream{ChatID: chatID, Thread: thread}
}

// Resolve removes an id (its finalize marker was emitted).
func (t *StreamIDTracker) Resolve(id string) {
	if t == nil {
		return
	}
	delete(t.outstanding, id)
}

// OutstandingIDs returns the not-yet-finalized ids in sorted order.
// Sorted so cleanup schedules activities deterministically across replays.
func (t *StreamIDTracker) OutstandingIDs() []string {
	if t == nil {
		return nil
	}
	ids := make([]string, 0, len(t.outstanding))
	for id := range t.outstanding {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// preallocateAssistantMessageID mints a message id for an imminent CallLLM
// dispatch and registers it as outstanding. Returns "" when replaying a
// pre-change history (DefaultVersion) — callers key every downstream
// behavior on the id being non-empty, which keeps old histories command-free.
func preallocateAssistantMessageID(ctx workflow.Context, chatID, thread string) string {
	if workflow.GetVersion(ctx, streamFinalizedVersionGate, workflow.DefaultVersion, 1) < 1 {
		return ""
	}
	var id string
	if err := workflow.SideEffect(ctx, func(workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&id); err != nil {
		workflow.GetLogger(ctx).Warn("[StreamFinalized] SideEffect for message id failed", "error", err)
		return ""
	}
	streamIDTrackerFrom(ctx).Register(id, chatID, thread)
	return id
}

// emitStreamFinalized fires the EmitStreamFinalized activity for an allocated
// id and resolves it in the tracker. Modeled on notifyWorkflowStatus:
// short-timeout, best-effort (a failed marker write is logged, never fails
// the workflow), and switches to a disconnected context when the workflow
// context is already cancelled so cancellation cleanup can still emit.
// No-op when messageID is empty (legacy histories / non-preallocated calls).
func emitStreamFinalized(ctx workflow.Context, chatID, messageID, thread, reason string, lastStreamSeq int64) {
	if messageID == "" {
		return
	}
	logger := workflow.GetLogger(ctx)

	baseCtx := ctx
	if ctx.Err() != nil {
		baseCtx, _ = workflow.NewDisconnectedContext(ctx)
	}

	activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})

	// Map input with snake_case keys (same pattern as notifyWorkflowStatus)
	// to avoid an import cycle with the handlers package.
	input := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"thread":     thread,
		"reason":     reason,
	}
	if lastStreamSeq > 0 {
		input["last_stream_seq"] = lastStreamSeq
	}

	var result map[string]interface{}
	if err := workflow.ExecuteActivity(activityCtx, "EmitStreamFinalized", input).Get(baseCtx, &result); err != nil {
		logger.Warn("[StreamFinalized] Failed to emit stream_finalized marker",
			"messageID", messageID,
			"reason", reason,
			"error", err)
	}

	// Resolved regardless of activity outcome: retrying the marker at
	// workflow-completion would just fail the same way.
	streamIDTrackerFrom(ctx).Resolve(messageID)
}

// finalizeOutstandingStreams emits aborted markers for every id that was
// allocated but never finalized. Called from handleWorkflowCompletion on the
// cancel / error / panic paths with a disconnected context.
func finalizeOutstandingStreams(ctx workflow.Context, reason string) {
	tracker := streamIDTrackerFrom(ctx)
	if tracker == nil {
		return
	}
	for _, id := range tracker.OutstandingIDs() {
		info := tracker.outstanding[id]
		emitStreamFinalized(ctx, info.ChatID, id, info.Thread, reason, 0)
	}
}

// extractLastStreamSeq pulls last_stream_seq out of a normalized CallLLM
// output map. The flexible data converter decodes proto outputs via
// protojson, which serializes int64 as a JSON STRING; plain JSON paths yield
// float64. Handle all the shapes.
func extractLastStreamSeq(output map[string]interface{}) int64 {
	if output == nil {
		return 0
	}
	switch v := output["last_stream_seq"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return 0
}
