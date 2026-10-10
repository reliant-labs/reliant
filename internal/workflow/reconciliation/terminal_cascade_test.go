// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Prod, 2026-10-10: an operator terminated chat 66a045ce in Temporal at
// 14:45:28. The reconciler's status-drift repair moved the root workflow to
// failed — and nothing else. Its thread stayed running, so the next pass, 30s
// later, reaped it and logged "Reaped orphaned threads — a terminal workflow
// did not cascade to its thread" at ERROR, which paged.
//
// The wedge repair already cascaded; every other repair the reconciler makes
// (lost run, stuck-task terminate, progress-stall terminate, drift while
// running, drift while paused) moved the root alone. Every one of them now
// goes through swapStatus, which ends the subtree in the same commit.

func TestReconciler_EveryTerminalRepairCascadesInTheSameCommit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dbStatus   db.WorkflowStatus
		temporal   *mockDescribeResponse // nil = not found in Temporal
		wantStatus db.WorkflowStatus
	}{
		{
			name:     "operator terminate while running (chat 66a045ce)",
			dbStatus: db.Active(), temporal: &mockDescribeResponse{resp: makeTerminatedDescribeResp("run-1")},
			wantStatus: db.Failed(),
		},
		{
			name:     "completed without reporting it",
			dbStatus: db.Active(), temporal: &mockDescribeResponse{resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)},
			wantStatus: db.Completed(),
		},
		{
			name:     "cancelled without reporting it",
			dbStatus: db.Active(), temporal: &mockDescribeResponse{resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_CANCELED)},
			wantStatus: db.Cancelled(),
		},
		{
			name:     "terminated while paused",
			dbStatus: db.Paused(), temporal: &mockDescribeResponse{resp: makeTerminatedDescribeResp("run-1")},
			wantStatus: db.Failed(),
		},
		{
			name:     "timed out while paused",
			dbStatus: db.Paused(), temporal: &mockDescribeResponse{resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT)},
			wantStatus: db.Failed(),
		},
		{
			name:       "lost from Temporal",
			dbStatus:   db.Active(),
			wantStatus: db.Completed(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockRepo()
			tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}
			if tc.temporal != nil {
				tempClient.describeResponses["wf-1"] = *tc.temporal
			}
			wf := runningWorkflow()
			wf.Status = tc.dbStatus

			result := NewReconciler(repo, tempClient, DefaultConfig()).ReconcileWorkflow(context.Background(), wf)

			require.NoError(t, result.Error)
			require.Equal(t, tc.wantStatus, repo.updatedStatuses["wf-1"], "the root is repaired")
			assertSubtreeEnded(t, repo, tc.wantStatus.StopReason)
		})
	}
}

// The stuck-task fallback terminates the run itself, so no workflow code will
// ever cascade for it either.
func TestReconciler_StuckTerminateCascadesInTheSameCommit(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
		resetErr:      assert.AnError,
	}
	tempClient.setPollersActive(true)
	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	require.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
	require.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	assertSubtreeEnded(t, repo, db.StopReasonFailed)
}

// A drift that does not end the run must not drain its subtree.
func TestReconciler_NonTerminalRepairDoesNotCascade(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{"wf-1": {resp: makeRunningDescribeResp("run-1")}},
	}
	wf := runningWorkflow()
	wf.Status = db.Paused()

	result := NewReconciler(repo, tempClient, DefaultConfig()).ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.Empty(t, repo.cascadedDescendants)
	assert.Empty(t, repo.cascadedThreadSubtrees)
}

func assertSubtreeEnded(t *testing.T, repo *mockRepo, reason db.WorkflowStopReason) {
	t.Helper()
	assert.Equal(t, []string{"wf-1"}, repo.cascadedDescendants, "the run's sub-agent workflows end with it")
	assert.Equal(t, []string{"wf-1"}, repo.cascadedThreadSubtrees, "the run's threads end with it")
	assert.Equal(t, reason, repo.cascadedReason, "descendants inherit the run's own stop reason")
	assert.Equal(t, reason, repo.cascadedThreadReason, "threads inherit the run's own stop reason")
	assert.Equal(t, []bool{true, true}, repo.cascadedInTx,
		"the cascade commits with the status: no pass may see the root terminal and its threads running")
}

// --- The reap's level follows how the run ended ---

// recordedLog is one slog record a test captured.
type recordedLog struct {
	level   slog.Level
	message string
	attrs   map[string]any
}

type recordingHandler struct {
	mu      *sync.Mutex
	records *[]recordedLog
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h recordingHandler) WithGroup(string) slog.Handler            { return h }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, recordedLog{level: r.Level, message: r.Message, attrs: attrs})
	return nil
}

// captureLogs routes the default logger into a buffer for the test.
func captureLogs(t *testing.T) func() []recordedLog {
	t.Helper()
	var mu sync.Mutex
	var records []recordedLog
	previous := slog.Default()
	slog.SetDefault(slog.New(recordingHandler{mu: &mu, records: &records}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []recordedLog {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedLog(nil), records...)
	}
}

func reapLogs(records []recordedLog) []recordedLog {
	var out []recordedLog
	for _, record := range records {
		if record.message == reapedAfterTerminateMessage || record.message == reapedOrphanedThreadsMessage {
			out = append(out, record)
		}
	}
	return out
}

const (
	reapedAfterTerminateMessage  = "[Reconciler] Reaped the threads of a run Temporal stopped outright — no workflow code ran to cascade, as expected"
	reapedOrphanedThreadsMessage = "[Reconciler] Reaped orphaned threads — a terminal workflow did not cascade to its thread"
)

// A terminate runs no workflow code, so a thread left running behind one is
// the reap working as designed: INFO, not a page.
func TestReconciler_ReapAfterTerminateIsInfo(t *testing.T) {
	logs := captureLogs(t)
	repo := newMockRepo()
	repo.reapedThreads = []db.ReapedThread{
		{ThreadID: "66a045ce-5872-4efe-a510-910f63fcb8a6", ChatID: "66a045ce-5872-4efe-a510-910f63fcb8a6", WorkflowID: "66a045ce-5872-4efe-a510-910f63fcb8a6", Status: db.ThreadStatusFailed},
	}
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"66a045ce-5872-4efe-a510-910f63fcb8a6": {resp: makeTerminatedDescribeResp("run-1")},
	}}
	before := anomalyCount(anomalyOrphanThreadReapedAfterTerminate)
	faultsBefore := anomalyCount(anomalyOrphanThreadReaped)

	reconciled, errs := NewReconciler(repo, tempClient, nil).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, 1, reconciled, "the repair still counts as one")

	reaps := reapLogs(logs())
	require.Len(t, reaps, 1)
	assert.Equal(t, slog.LevelInfo, reaps[0].level, "a reap after a terminate must not page")
	assert.Equal(t, reapedAfterTerminateMessage, reaps[0].message)
	assert.Equal(t, "66a045ce-5872-4efe-a510-910f63fcb8a6", reaps[0].attrs["workflowID"])
	assert.Equal(t, "66a045ce-5872-4efe-a510-910f63fcb8a6", reaps[0].attrs["chatID"])
	assert.Equal(t, "66a045ce-5872-4efe-a510-910f63fcb8a6", reaps[0].attrs["threadID"])
	assert.Equal(t, "Terminated", reaps[0].attrs["closeStatus"])
	assert.Equal(t, 1.0, anomalyCount(anomalyOrphanThreadReapedAfterTerminate)-before)
	assert.Equal(t, 0.0, anomalyCount(anomalyOrphanThreadReaped)-faultsBefore,
		"the fault class counts only real missed cascades")
}

// A run that ended by running its own code had every chance to cascade. A
// thread left behind is a real bug: ERROR, with the run's root, chat, threads
// and close status. A sub-agent's thread is attributed to its run's ROOT —
// the execution Temporal knows — not to its own inline workflow row.
func TestReconciler_ReapAfterNormalCompletionIsAnError(t *testing.T) {
	logs := captureLogs(t)
	repo := newMockRepo()
	root := "root-1"
	repo.workflowRows = map[string]*db.Workflow{
		"root-1":  {ID: "root-1", ChatID: "chat-1", Status: db.Completed()},
		"child-1": {ID: "child-1", ParentID: &root, ChatID: "chat-1", Status: db.Completed()},
	}
	repo.reapedThreads = []db.ReapedThread{
		{ThreadID: "root-1", ChatID: "chat-1", WorkflowID: "root-1", Status: db.ThreadStatusCompleted},
		{ThreadID: "child-1", ChatID: "chat-1", WorkflowID: "child-1", Status: db.ThreadStatusCompleted},
	}
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"root-1": {resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)},
	}}
	before := anomalyCount(anomalyOrphanThreadReaped)

	_, errs := NewReconciler(repo, tempClient, nil).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	reaps := reapLogs(logs())
	require.Len(t, reaps, 1, "one run, one log line, however many of its threads were reaped")
	assert.Equal(t, slog.LevelError, reaps[0].level, "a missed cascade after a normal completion is a bug")
	assert.Equal(t, reapedOrphanedThreadsMessage, reaps[0].message)
	assert.Equal(t, "root-1", reaps[0].attrs["workflowID"])
	assert.Equal(t, "chat-1", reaps[0].attrs["chatID"])
	assert.Equal(t, []string{"root-1", "child-1"}, reaps[0].attrs["threadIDs"])
	assert.Equal(t, []string{"root-1", "child-1"}, reaps[0].attrs["threadWorkflowIDs"])
	assert.Equal(t, int64(2), reaps[0].attrs["rows"])
	assert.Equal(t, "Completed", reaps[0].attrs["closeStatus"])
	assert.Equal(t, 2.0, anomalyCount(anomalyOrphanThreadReaped)-before)
}

// When Temporal cannot say how the run ended, the reap is not explained away.
func TestReconciler_ReapOfUnknownRunIsAnError(t *testing.T) {
	logs := captureLogs(t)
	repo := newMockRepo()
	repo.reapedThreads = []db.ReapedThread{{ThreadID: "th-1", ChatID: "chat-1", WorkflowID: "gone", Status: db.ThreadStatusFailed}}

	_, errs := NewReconciler(repo, &mockReconcilerTemporalClient{}, nil).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	reaps := reapLogs(logs())
	require.Len(t, reaps, 1)
	assert.Equal(t, slog.LevelError, reaps[0].level)
	assert.Equal(t, "unknown", reaps[0].attrs["closeStatus"])
}
