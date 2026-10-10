// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errclass"
)

// Sentry after an operator terminated a wedged run (#689): the reconciler's
// repair of it logged "Workflow ended terminally without reporting it" — and,
// had any descendant been left running, "Reaped orphaned workflow
// descendants" — at ERROR. A terminate (or a timeout) runs no workflow code,
// so nothing could have reported or cascaded the end: the repair is the
// designed recovery, and logging it as a fault pages for nothing. Each is
// levelled by how Temporal says the run ended, as #689 did for the thread
// reap.

const (
	silentEndMessage       = "[Reconciler] Workflow ended terminally without reporting it - notifying user"
	pausedSilentEndMessage = "[Reconciler] Paused workflow ended terminally without reporting it - notifying user"

	reapedDescendantsAfterTerminateMessage = "[Reconciler] Reaped the descendants of a run Temporal stopped outright — no workflow code ran to cascade, as expected"
	reapedOrphanedDescendantsMessage       = "[Reconciler] Reaped orphaned workflow descendants — a terminal parent did not cascade"
)

// captureClassifiedLogs is captureLogs behind the process's error-class
// policy (internal/logging.withReporting): a record carrying a user error is
// written at INFO with error_class=user, as it is in production.
func captureClassifiedLogs(t *testing.T) func() []recordedLog {
	t.Helper()
	var mu sync.Mutex
	var records []recordedLog
	previous := slog.Default()
	slog.SetDefault(slog.New(observe.NewErrorClassHandler(
		recordingHandler{mu: &mu, records: &records},
		observe.WithErrorClassifier(errclass.Library),
	)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []recordedLog {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedLog(nil), records...)
	}
}

func logsWithMessage(records []recordedLog, messages ...string) []recordedLog {
	var out []recordedLog
	for _, record := range records {
		for _, message := range messages {
			if record.message == message {
				out = append(out, record)
			}
		}
	}
	return out
}

// failedCloseEvent is makeReplayedFailureCloseEvent with its root cause's
// category set: BENIGN is how the activity boundary marks a user error.
func failedCloseEvent(rootCategory enums.ApplicationErrorCategory) []*historypb.HistoryEvent {
	events := makeReplayedFailureCloseEvent()
	f := events[0].GetWorkflowExecutionFailedEventAttributes().GetFailure()
	for f.GetCause() != nil {
		f = f.GetCause()
	}
	f.GetApplicationFailureInfo().Category = rootCategory
	return events
}

func TestReconciler_UnreportedEndIsLevelledByHowTheRunEnded(t *testing.T) {
	for _, run := range []struct {
		name     string
		dbStatus db.WorkflowStatus
		message  string
	}{
		{"running", db.Active(), silentEndMessage},
		{"paused", db.Paused(), pausedSilentEndMessage},
	} {
		for _, tc := range []struct {
			name        string
			close       enums.WorkflowExecutionStatus
			history     []*historypb.HistoryEvent
			wantLevel   slog.Level
			wantClosed  string
			wantClass   string // anomaly class counted
			wantUserErr bool
		}{
			{
				name:  "operator terminate",
				close: enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, history: makeTerminatedCloseEvent("terminated by operator: replay wedge"),
				wantLevel: slog.LevelInfo, wantClosed: "Terminated", wantClass: anomalySilentTerminalDriftExpected,
			},
			{
				name:      "execution timeout",
				close:     enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
				wantLevel: slog.LevelInfo, wantClosed: "TimedOut", wantClass: anomalySilentTerminalDriftExpected,
			},
			{
				name:  "failed on a server fault",
				close: enums.WORKFLOW_EXECUTION_STATUS_FAILED, history: failedCloseEvent(enums.APPLICATION_ERROR_CATEGORY_UNSPECIFIED),
				wantLevel: slog.LevelError, wantClosed: "Failed", wantClass: anomalySilentTerminalDrift,
			},
			{
				name:  "failed on the user's error",
				close: enums.WORKFLOW_EXECUTION_STATUS_FAILED, history: failedCloseEvent(enums.APPLICATION_ERROR_CATEGORY_BENIGN),
				wantLevel: slog.LevelInfo, wantClosed: "Failed", wantClass: anomalySilentTerminalDriftExpected, wantUserErr: true,
			},
		} {
			t.Run(run.name+"/"+tc.name, func(t *testing.T) {
				logs := captureClassifiedLogs(t)
				wf := terminatedChatWorkflow()
				wf.Status = run.dbStatus
				repo := newTerminationRepo(wf)
				describe := makeTerminalDescribeResp(tc.close)
				if tc.close == enums.WORKFLOW_EXECUTION_STATUS_TERMINATED {
					describe = makeTerminatedDescribeResp("run-1")
				}
				tempClient := &mockReconcilerTemporalClient{
					describeResponses: map[string]mockDescribeResponse{"wf-1": {resp: describe}},
					historyEvents:     tc.history,
				}
				expectedBefore := anomalyCount(anomalySilentTerminalDriftExpected)
				faultsBefore := anomalyCount(anomalySilentTerminalDrift)

				_, errs := NewReconciler(repo, tempClient, DefaultConfig()).ReconcileRunningWorkflows(context.Background())
				require.Empty(t, errs)

				lines := logsWithMessage(logs(), run.message)
				require.Len(t, lines, 1)
				assert.Equal(t, tc.wantLevel, lines[0].level)
				assert.Equal(t, tc.wantClosed, lines[0].attrs["closeStatus"], "the line says how Temporal ended the run")
				assert.Equal(t, "wf-1", lines[0].attrs["workflowID"])
				assert.Equal(t, "chat-1", lines[0].attrs["chatID"])
				if tc.wantUserErr {
					assert.Equal(t, "user", lines[0].attrs[observe.ErrorClassKey],
						"#686: the user's error is classified, logged at INFO and kept out of Sentry")
				}

				wantExpected, wantFaults := 0.0, 0.0
				if tc.wantClass == anomalySilentTerminalDrift {
					wantFaults = 1
				} else {
					wantExpected = 1
				}
				assert.Equal(t, wantExpected, anomalyCount(anomalySilentTerminalDriftExpected)-expectedBefore)
				assert.Equal(t, wantFaults, anomalyCount(anomalySilentTerminalDrift)-faultsBefore,
					"the fault class counts only ends a run's own code should have reported")
				assert.Len(t, repo.errorUpdates(), 1, "the user is told either way")
			})
		}
	}
}

func reapedWorkflows(n int) []db.ReapedWorkflow {
	rows := make([]db.ReapedWorkflow, n)
	for i := range rows {
		rows[i] = db.ReapedWorkflow{WorkflowID: fmt.Sprintf("child-%d", i+1), ChatID: "chat-1", StopReason: db.StopReasonFailed}
	}
	return rows
}

// descendantReapRepo is a run root with a reaped child, as the reap returns it.
func descendantReapRepo() *mockRepo {
	repo := newMockRepo()
	root := "root-1"
	repo.workflowRows = map[string]*db.Workflow{
		"root-1":  {ID: "root-1", ChatID: "chat-1", Status: db.Failed()},
		"child-1": {ID: "child-1", ParentID: &root, ChatID: "chat-1", Status: db.Failed()},
	}
	repo.reapRows = []db.ReapedWorkflow{{WorkflowID: "child-1", ChatID: "chat-1", StopReason: db.StopReasonFailed}}
	return repo
}

// A descendant left running under a run Temporal stopped outright is the reap
// working as designed: INFO, with the run's root, chat, rows and close status.
func TestReconciler_DescendantReapAfterTerminateIsInfo(t *testing.T) {
	logs := captureLogs(t)
	repo := descendantReapRepo()
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"root-1": {resp: makeTerminatedDescribeResp("run-1")},
	}}
	expectedBefore := anomalyCount(anomalyOrphanDescendantReapedAfterTerminate)
	faultsBefore := anomalyCount(anomalyOrphanDescendantReaped)

	reconciled, errs := NewReconciler(repo, tempClient, nil).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, 1, reconciled, "the repair still counts")

	reaps := logsWithMessage(logs(), reapedDescendantsAfterTerminateMessage, reapedOrphanedDescendantsMessage)
	require.Len(t, reaps, 1)
	assert.Equal(t, slog.LevelInfo, reaps[0].level, "a reap after a terminate must not page")
	assert.Equal(t, reapedDescendantsAfterTerminateMessage, reaps[0].message)
	assert.Equal(t, "root-1", reaps[0].attrs["workflowID"], "attributed to the run's root, the execution Temporal knows")
	assert.Equal(t, "chat-1", reaps[0].attrs["chatID"])
	assert.Equal(t, []string{"child-1"}, reaps[0].attrs["workflowIDs"])
	assert.Equal(t, int64(1), reaps[0].attrs["rows"])
	assert.Equal(t, "Terminated", reaps[0].attrs["closeStatus"])
	assert.Equal(t, 1.0, anomalyCount(anomalyOrphanDescendantReapedAfterTerminate)-expectedBefore)
	assert.Equal(t, 0.0, anomalyCount(anomalyOrphanDescendantReaped)-faultsBefore,
		"the fault class counts only real missed cascades")
}

// A run that ended by running its own code had every chance to cascade.
func TestReconciler_DescendantReapAfterTheRunsOwnEndIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		describe   map[string]mockDescribeResponse
		wantClosed string
	}{
		{"failed", map[string]mockDescribeResponse{"root-1": {resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_FAILED)}}, "Failed"},
		{"Temporal cannot say", nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			repo := descendantReapRepo()
			faultsBefore := anomalyCount(anomalyOrphanDescendantReaped)

			_, errs := NewReconciler(repo, &mockReconcilerTemporalClient{describeResponses: tc.describe}, nil).ReconcileRunningWorkflows(context.Background())
			require.Empty(t, errs)

			reaps := logsWithMessage(logs(), reapedDescendantsAfterTerminateMessage, reapedOrphanedDescendantsMessage)
			require.Len(t, reaps, 1)
			assert.Equal(t, slog.LevelError, reaps[0].level)
			assert.Equal(t, reapedOrphanedDescendantsMessage, reaps[0].message)
			assert.Equal(t, "root-1", reaps[0].attrs["workflowID"])
			assert.Equal(t, tc.wantClosed, reaps[0].attrs["closeStatus"])
			assert.Equal(t, 1.0, anomalyCount(anomalyOrphanDescendantReaped)-faultsBefore)
		})
	}
}
