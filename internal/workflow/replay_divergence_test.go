// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"errors"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run whose recorded history the current code can no longer replay must
// never be reset-and-replayed. A reset replays the history from event 1 with
// the current code, so it diverges at the same event and wedges again.
//
// The shape is chat 97654413 (2026-10-09): a run started on the old startup
// order (PreflightDaemonCheck scheduled as activity 11), parked; the deploy of
// #641 moved preflight after the "started" WorkflowStatus; the user's next
// message made the new worker replay the history, and every workflow task
// failed TMPRL1100. The run was then terminated to unwedge it.

func workflowTaskFailed(eventID int64, cause enumspb.WorkflowTaskFailedCause) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   eventID,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskFailedEventAttributes{
			WorkflowTaskFailedEventAttributes: &historypb.WorkflowTaskFailedEventAttributes{Cause: cause},
		},
	}
}

func signaled(eventID int64, name string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   eventID,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionSignaledEventAttributes{
			WorkflowExecutionSignaledEventAttributes: &historypb.WorkflowExecutionSignaledEventAttributes{SignalName: name},
		},
	}
}

// replayDivergedThenTerminatedHistory is the wedged-then-terminated run, in
// forward order.
func replayDivergedThenTerminatedHistory() []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeActivityScheduled(5), // ActivityLoadWorkflow
		makeActivityCompleted(7, 5),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeActivityScheduled(11), // PreflightDaemonCheck, on the old order
		makeActivityCompleted(13, 11),
		makeEvent(11142, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED), // parked
		signaled(11143, "update_workflow_state"),
		makeEvent(11144, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(11145, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		workflowTaskFailed(11146, enumspb.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
		signaled(11147, "signal.resume"),
		signaled(11148, "thread_wake"),
		makeEvent(11150, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(11151, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		workflowTaskFailed(11152, enumspb.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
		makeEvent(11153, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED),
	}
}

func TestFindResumeResetPoint_ReplayDivergedRun_RefusesToReset(t *testing.T) {
	mc := &mockTemporalClient{historyIter: &mockHistoryIterator{events: replayDivergedThenTerminatedHistory()}}

	_, err := findResumeResetPoint(context.Background(), mc, "wf-1", "run-1", enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	assert.ErrorIs(t, err, ErrReplayDiverged,
		"every reset point re-diverges on replay; the run must go to the coarse fresh restart")
}

func TestResetInterruptedWorkflow_ReplayDivergedRun_NeverCallsReset(t *testing.T) {
	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: replayDivergedThenTerminatedHistory()},
		resetResp:   &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}

	_, err := ResetInterruptedWorkflow(context.Background(), mc, "wf-1", "old-run", enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	assert.ErrorIs(t, err, ErrReplayDiverged)
	assert.False(t, mc.resetCalled, "a reset of a diverged history wedges the new run exactly like the old one")
}

// A divergence that a later workflow task got past is not a divergence any
// more: something (a fixing deploy) made the history replay again.
func TestFindResumeResetPoint_DivergenceFollowedByCompletedTask_StillResets(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		workflowTaskFailed(7, enumspb.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(11, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED),
	}
	mc := &mockTemporalClient{historyIter: &mockHistoryIterator{events: events}}

	point, err := findResumeResetPoint(context.Background(), mc, "wf-1", "run-1", enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	require.NoError(t, err)
	assert.Equal(t, int64(10), point.eventID)
}

// A workflow task that TIMED OUT is a slow worker, not code that cannot
// replay. It is the 2026-09-29 false-wedge shape and must stay resettable.
func TestFindResumeResetPoint_TimedOutWorkflowTask_StillResets(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(7, enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED),
	}
	mc := &mockTemporalClient{historyIter: &mockHistoryIterator{events: events}}

	point, err := findResumeResetPoint(context.Background(), mc, "wf-1", "run-1", enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	require.NoError(t, err)
	assert.Equal(t, int64(4), point.eventID)
}

func TestResumeInterruptedWorkflow_ReplayDiverged_FallsBackWithoutReset(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "old-run", 11153),
		historyEvents: replayDivergedThenTerminatedHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1", nil)
	assert.ErrorIs(t, err, ErrReplayDiverged)
	assert.False(t, tc.resetCalled)
}

// --- LatestWorkflowTaskOutcome: the reverse read the reconciler uses ---

// reverseHistoryReader serves a run's history newest-first in pages, the way
// GetWorkflowExecutionHistoryReverse does.
type reverseHistoryReader struct {
	forward  []*historypb.HistoryEvent
	pageSize int
	calls    int
}

func (r *reverseHistoryReader) GetWorkflowExecutionHistoryReverse(
	_ context.Context, req *workflowservice.GetWorkflowExecutionHistoryReverseRequest, _ ...grpc.CallOption,
) (*workflowservice.GetWorkflowExecutionHistoryReverseResponse, error) {
	r.calls++
	start := len(r.forward) - 1
	if len(req.GetNextPageToken()) > 0 {
		start = int(req.GetNextPageToken()[0])
	}
	var page []*historypb.HistoryEvent
	i := start
	for ; i >= 0 && len(page) < r.pageSize; i-- {
		page = append(page, r.forward[i])
	}
	resp := &workflowservice.GetWorkflowExecutionHistoryReverseResponse{
		History: &historypb.History{Events: page},
	}
	if i >= 0 {
		resp.NextPageToken = []byte{byte(i)}
	}
	return resp, nil
}

func TestLatestWorkflowTaskOutcome_WedgedRun_ReportsReplayDivergence(t *testing.T) {
	// Still running: the same history without the terminate.
	events := replayDivergedThenTerminatedHistory()
	events = events[:len(events)-1]
	reader := &reverseHistoryReader{forward: events, pageSize: 3}

	outcome, err := LatestWorkflowTaskOutcome(context.Background(), reader, "default", "wf-1", "run-1")
	require.NoError(t, err)
	assert.True(t, outcome.ReplayDiverged())
	assert.True(t, outcome.FailsDeterministically())
	assert.False(t, outcome.CancelRequested)
	assert.Equal(t, int64(11152), outcome.EventID)
}

func TestLatestWorkflowTaskOutcome_CancelRequestedSinceLastCompletedTask(t *testing.T) {
	// Chat 3f03dc31: archiving the chat requested a cancel, and the workflow
	// task that would have delivered it wedged.
	events := []*historypb.HistoryEvent{
		makeEvent(26752, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(26753, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCEL_REQUESTED),
		makeEvent(26754, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(26755, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		workflowTaskFailed(26756, enumspb.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
	}
	reader := &reverseHistoryReader{forward: events, pageSize: 100}

	outcome, err := LatestWorkflowTaskOutcome(context.Background(), reader, "default", "wf-1", "run-1")
	require.NoError(t, err)
	assert.True(t, outcome.ReplayDiverged())
	assert.True(t, outcome.CancelRequested, "the cancel was never delivered, so the run's owner asked for it to stop")
}

func TestLatestWorkflowTaskOutcome_TimedOutTask_IsNotAFailure(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		signaled(5, "thread_wake"),
		makeEvent(6, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(7, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(8, enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT),
	}
	reader := &reverseHistoryReader{forward: events, pageSize: 100}

	outcome, err := LatestWorkflowTaskOutcome(context.Background(), reader, "default", "wf-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT, outcome.EventType)
	assert.False(t, outcome.FailsDeterministically(), "a slow host is not a wedge")
}

func TestLatestWorkflowTaskOutcome_UnhandledWorkflowPanic_FailsDeterministicallyButIsNotADivergence(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		workflowTaskFailed(8, enumspb.WORKFLOW_TASK_FAILED_CAUSE_WORKFLOW_WORKER_UNHANDLED_FAILURE),
	}
	reader := &reverseHistoryReader{forward: events, pageSize: 100}

	outcome, err := LatestWorkflowTaskOutcome(context.Background(), reader, "default", "wf-1", "run-1")
	require.NoError(t, err)
	assert.True(t, outcome.FailsDeterministically())
	assert.False(t, outcome.ReplayDiverged())
}

func TestLatestWorkflowTaskOutcome_ReaderError(t *testing.T) {
	_, err := LatestWorkflowTaskOutcome(context.Background(), failingReverseReader{}, "default", "wf-1", "run-1")
	assert.Error(t, err)
}

type failingReverseReader struct{}

func (failingReverseReader) GetWorkflowExecutionHistoryReverse(
	context.Context, *workflowservice.GetWorkflowExecutionHistoryReverseRequest, ...grpc.CallOption,
) (*workflowservice.GetWorkflowExecutionHistoryReverseResponse, error) {
	return nil, errors.New("temporal unavailable")
}
