// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A hard Temporal TERMINATE is the one closure a workflow can never report:
// the worker receives no further workflow task, so the completion handler,
// the status activity and the error activity all never run. Before this
// suite existed the reconciler repaired the DB status in silence and the UI
// went on showing dead agents as running
// (docs/incidents/2026-08-12-spawn-history-cap.md).

// CreateChatUpdate on the shared mockRepo is a deliberate no-op. Any test
// whose workflow drifts to a terminal failed status now reaches the emit
// path, and mockRepo's embedded db.Repository is a nil interface, so every
// method the pass calls must exist here or it panics. Tests that care about
// the emitted error use terminationRepo below, which records instead.
func (m *mockRepo) CreateChatUpdate(_ context.Context, _ string, _ reliantv1.ChatUpdateType, _ string, _ string) error {
	return nil
}

// terminationRepo records the chat_updates rows the reconciler writes, and
// models CompareAndSwapWorkflowStatus with REAL CAS semantics so a
// multi-pass test behaves like the live reconcile loop: the pass that wins
// the transition is the only one that sees a mismatch.
//
// It embeds *mockRepo rather than extending it so the shared mock in
// reconciler_test.go stays untouched.
type terminationRepo struct {
	*mockRepo

	mu          sync.Mutex
	rows        map[string]*db.Workflow // workflowID -> current row (status is live)
	chatUpdates []recordedChatUpdate
}

type recordedChatUpdate struct {
	chatID     string
	updateType reliantv1.ChatUpdateType
	entityID   string
	data       map[string]any
}

func newTerminationRepo(workflows ...*db.Workflow) *terminationRepo {
	r := &terminationRepo{
		mockRepo: newMockRepo(),
		rows:     make(map[string]*db.Workflow, len(workflows)),
	}
	for _, wf := range workflows {
		r.rows[wf.ID] = wf
	}
	return r
}

func (r *terminationRepo) ListWorkflowsByStatus(_ context.Context, status db.WorkflowStatus) ([]*db.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*db.Workflow
	for _, wf := range r.rows {
		if wf.Status == status {
			out = append(out, wf)
		}
	}
	return out, nil
}

// CompareAndSwapWorkflowStatus swaps only when the row still holds the
// expected status — the property the whole idempotency guarantee rests on.
func (r *terminationRepo) CompareAndSwapWorkflowStatus(_ context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wf, ok := r.rows[id]
	if !ok || wf.Status != expectedStatus {
		return false, nil
	}
	wf.Status = newStatus
	return true, nil
}

func (r *terminationRepo) CreateChatUpdate(_ context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chatUpdates = append(r.chatUpdates, recordedChatUpdate{
		chatID:     chatID,
		updateType: updateType,
		entityID:   entityID,
		data:       parsed,
	})
	return nil
}

func (r *terminationRepo) errorUpdates() []recordedChatUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedChatUpdate
	for _, u := range r.chatUpdates {
		if u.updateType == db.UpdateTypeError {
			out = append(out, u)
		}
	}
	return out
}

// makeTerminatedDescribeResp is makeTerminalDescribeResp's TERMINATED
// variant, kept separate because only this status carries a reason event.
func makeTerminatedDescribeResp(runID string) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:        enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			Execution:     &commonpb.WorkflowExecution{RunId: runID},
			HistoryLength: 51199,
		},
	}
}

// makeTerminatedCloseEvent is the close event Temporal wrote for the
// incident this path exists for, reason and all.
func makeTerminatedCloseEvent(reason string) []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		{
			EventId:   51199,
			EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionTerminatedEventAttributes{
				WorkflowExecutionTerminatedEventAttributes: &historypb.WorkflowExecutionTerminatedEventAttributes{
					Reason:   reason,
					Identity: "history-service",
				},
			},
		},
	}
}

func terminatedChatWorkflow() *db.Workflow {
	return &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Thread:       "thread-main",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}
}

func TestReconciler_HardTermination_EmitsUserVisibleError(t *testing.T) {
	repo := newTerminationRepo(terminatedChatWorkflow())
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent("Workflow history count exceeds limit."),
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	// The DB status is repaired to Failed, which is what keeps the position
	// checkpoint and routes the next message into resume-at-position.
	assert.Equal(t, db.WorkflowStatusFailed, repo.rows["wf-1"].Status)

	// ...and, unlike before, the user is told.
	updates := repo.errorUpdates()
	require.Len(t, updates, 1, "a terminated run must emit exactly one error to the user")

	update := updates[0]
	assert.Equal(t, "chat-1", update.chatID)

	data := update.data
	assert.Equal(t, "error", data["update_type"], "must match the frontend ErrorUpdate shape")
	assert.Equal(t, "chat-1", data["chat_id"])
	assert.Equal(t, "wf-1", data["workflow_id"])
	assert.Equal(t, silentTerminationErrorType, data["activity_type"])
	assert.NotEmpty(t, data["timestamp"])
	assert.Equal(t, update.entityID, data["id"], "entity id and payload id must agree")

	// Scoped to the THREAD, not the chat: a chat-scoped error renders inside
	// every thread of the chat, including spawns that had nothing to do with
	// it (see WorkflowErrorInput.Thread).
	assert.Equal(t, "thread-main", data["thread"])

	// Temporal's own reason survives to the user.
	assert.Contains(t, data["error_message"], "Workflow history count exceeds limit.")

	// The summary is what the timeline shows collapsed, so it carries the
	// three facts that matter: it stopped, it was not the user's doing, and
	// it resumes.
	summary, _ := data["error_summary"].(string)
	assert.Contains(t, summary, "stopped by the system")
	assert.Contains(t, summary, "not by you")
	assert.Contains(t, summary, "pick up where it left off")
}

func TestReconciler_HardTermination_ErrorEmittedOncePerRun(t *testing.T) {
	// The loop runs every ~30s and would otherwise re-emit forever. The CAS
	// is the guard: only the pass that wins the transition out of running
	// sees a mismatch at all.
	repo := newTerminationRepo(terminatedChatWorkflow())
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent("Workflow history count exceeds limit."),
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	for pass := 1; pass <= 2; pass++ {
		_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
		require.Empty(t, errs, "pass %d", pass)
	}

	assert.Len(t, repo.errorUpdates(), 1,
		"two reconcile passes over the same dead run must emit exactly one error")
}

func TestReconciler_HardTermination_WhilePaused_EmitsError(t *testing.T) {
	// A run parked on a pause that dies is if anything MORE surprising: the
	// user was waiting on a resume that will never come.
	wf := terminatedChatWorkflow()
	wf.Status = db.WorkflowStatusPaused
	repo := newTerminationRepo(wf)
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent("Workflow history count exceeds limit."),
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, db.WorkflowStatusFailed, repo.rows["wf-1"].Status)
	require.Len(t, repo.errorUpdates(), 1, "a paused run that was terminated must also notify")
}

func TestReconciler_HardTermination_ReasonUnavailable_StillEmits(t *testing.T) {
	// Reading the close event is best-effort enrichment. Losing the reason
	// must never cost the user the notification itself.
	repo := newTerminationRepo(terminatedChatWorkflow())
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: nil, // no close event available
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	updates := repo.errorUpdates()
	require.Len(t, updates, 1)
	assert.Contains(t, updates[0].data["error_message"], "stopped by the system")
	assert.NotContains(t, updates[0].data["error_message"], "Reason:",
		"no reason available means no empty Reason clause")
}

func TestReconciler_CleanCompletionDrift_EmitsNoError(t *testing.T) {
	// A run that merely finished before its status activity landed is not a
	// failure, and telling the user their chat broke would be a lie.
	repo := newTerminationRepo(terminatedChatWorkflow())
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, db.WorkflowStatusCompleted, repo.rows["wf-1"].Status)
	assert.Empty(t, repo.errorUpdates(), "a completed run must not report an error")
}

func TestReconciler_UserCancelledDrift_EmitsNoError(t *testing.T) {
	// The user asked for this one. Reporting it back to them as a system
	// failure is noise.
	repo := newTerminationRepo(terminatedChatWorkflow())
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_CANCELED)},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, db.WorkflowStatusCancelled, repo.rows["wf-1"].Status)
	assert.Empty(t, repo.errorUpdates(), "a user cancel must not report an error")
}
