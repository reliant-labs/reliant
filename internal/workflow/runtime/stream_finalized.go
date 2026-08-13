// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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

// streamFinalizedLocalGate is the GetVersion changeID that switches the
// SUCCESS-path finalize marker from a regular activity (6 history events) to a
// local activity (1 marker event). Separate from streamFinalizedVersionGate
// because it is a strictly later, independent change: a history may already
// carry version 1 of the id-preallocation protocol and must keep replaying its
// recorded regular-activity commands, while new executions dispatch locally.
//
// Measured against a real 51,199-event history, EmitStreamFinalized ran 1,349
// times for 8,094 events — one per agent turn, second only to SaveMessage and
// the drain.
const streamFinalizedLocalGate = "stream-finalized-local"

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

	cancelled := ctx.Err() != nil
	baseCtx := ctx
	if cancelled {
		baseCtx, _ = workflow.NewDisconnectedContext(ctx)
	}

	input := types.EmitStreamFinalizedInput{
		ChatID:    chatID,
		MessageID: messageID,
		Thread:    thread,
		Reason:    reason,
	}
	if lastStreamSeq > 0 {
		input.LastStreamSeq = lastStreamSeq
	}

	// The marker is dispatched LOCALLY on the hot path and as a REGULAR
	// activity on the terminal path, and the split is deliberate.
	//
	// Local is safe on the hot path for the same reason the mailbox drain is:
	// this call is already best-effort (the error below is logged, never
	// propagated), and the marker's only content is a single chat_updates row
	// whose meaning is "no more deltas under this id". Losing an attempt to a
	// worker crash costs a UI hint, not state — and the frontend does not
	// depend on the marker alone: chatStore seeds its finalized-id set from
	// BOTH persisted stream_finalized markers AND every complete persisted
	// assistant message id, so a dropped marker is covered by the message
	// itself landing. Re-execution is equally harmless; a duplicate marker for
	// an id already in that set is a no-op.
	//
	// The terminal path is different in kind and stays a regular activity. A
	// local activity runs inside the current workflow task, so it needs the
	// workflow to get another workflow task to make progress — which is
	// exactly what a cancelled or completing execution is running out of.
	// finalizeOutstandingStreams calls this from handleWorkflowCompletion on
	// the cancel / error / panic paths precisely BECAUSE nothing else will
	// finalize those ids; that is the one case where "not durably scheduled"
	// changes the outcome from "retried later" to "never happened", leaving a
	// phantom streaming placeholder in the user's chat forever. A server-
	// scheduled activity on a disconnected context survives the workflow
	// closing. Those calls are also rare — measured at ONE WorkflowError in a
	// 51,199-event history — so they cost nothing to leave alone.
	useLocal := !cancelled &&
		workflow.GetVersion(ctx, streamFinalizedLocalGate, workflow.DefaultVersion, 1) >= 1

	var err error
	if useLocal {
		localCtx := workflow.WithLocalActivityOptions(baseCtx, workflow.LocalActivityOptions{
			ScheduleToCloseTimeout: 10 * time.Second,
			StartToCloseTimeout:    5 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    100 * time.Millisecond,
				BackoffCoefficient: 2.0,
				MaximumInterval:    time.Second,
				MaximumAttempts:    3,
			},
		})
		var result types.EmitStreamFinalizedOutput
		err = workflow.ExecuteLocalActivity(localCtx, "EmitStreamFinalized", input).Get(baseCtx, &result)
	} else {
		activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
			StartToCloseTimeout: 5 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    5 * time.Second,
				MaximumAttempts:    3,
			},
		})
		var result types.EmitStreamFinalizedOutput
		err = workflow.ExecuteActivity(activityCtx, "EmitStreamFinalized", input).Get(baseCtx, &result)
	}
	if err != nil {
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
