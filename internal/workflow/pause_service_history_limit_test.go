// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run at Temporal's per-execution history limit cannot be rescued by
// resetting: the reset forks from a point INSIDE the oversized history, so the
// new run is born at essentially the same event count and is terminated again
// within a few events.
//
// Measured on a real chat: terminated at 51,201 events; reset forked at 51,194;
// the new run started at 51,198, ran two steps, and died at 51,199. The user
// saw "send a message" produce one reply and stop. Resetting again would repeat
// it forever, so this must route to the coarse fresh restart instead.
func TestResumeInterruptedWorkflow_AtHistoryLimit_DoesNotReset(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "old-run", 51201),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrHistoryLimitExceeded)
	assert.False(t, tc.resetCalled,
		"resetting a run at the history cap forks from inside the oversized history and dies again")
}

// The headroom must catch a run that is merely NEAR the cap too: it has only a
// few events of room, which is not enough to make progress before Temporal
// terminates it.
func TestResumeInterruptedWorkflow_NearHistoryLimit_DoesNotReset(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", temporalHistoryCountLimit-temporalHistoryHeadroom),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrHistoryLimitExceeded)
	assert.False(t, tc.resetCalled)
}

// A normal interrupted run is unaffected — reset-and-replay is the precise
// recovery and must remain the default. This is the regression guard for the
// headroom being set too aggressively.
func TestResumeInterruptedWorkflow_NormalHistory_StillResets(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	newRunID, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.NoError(t, err)
	assert.Equal(t, "new-run", newRunID)
	assert.True(t, tc.resetCalled, "a normal interrupted run still gets the precise reset-and-replay recovery")
}

// terminatedByServerHistory is a TERMINATED run's history ending in the close
// event Temporal's history service writes when it kills a run at a limit.
func terminatedByServerHistory(reason string) []*historypb.HistoryEvent {
	events := failedByActivityHistory()
	events = events[:len(events)-1] // drop the FAILED close event
	return append(events, &historypb.HistoryEvent{
		EventId:   11,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionTerminatedEventAttributes{
			WorkflowExecutionTerminatedEventAttributes: &historypb.WorkflowExecutionTerminatedEventAttributes{
				Reason:   reason,
				Identity: "history-service",
			},
		},
	})
}

// Chat 0e15fdba died on SIZE at ~38k events — far below the count headroom
// check — and was reset back into its 52 MB history, dying again 2.5 minutes
// later. The close event names the limit, so it is the classifier.
func TestResumeInterruptedWorkflow_TerminatedForHistorySize_DoesNotReset(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "old-run", 38000),
		historyEvents: terminatedByServerHistory("Workflow history size exceeds limit."),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrHistoryLimitExceeded)
	assert.False(t, tc.resetCalled, "a size-limit death must never be reset into its own oversized history")
}

func TestResumeInterruptedWorkflow_TerminatedForHistoryCount_DoesNotReset(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "old-run", 20000),
		historyEvents: terminatedByServerHistory("Workflow history count exceeds limit."),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrHistoryLimitExceeded)
	assert.False(t, tc.resetCalled)
}

// The size backstop: a FAILED run (no terminate reason to read) whose history
// is within headroom of the 50 MB cap is just as unrecoverable by reset.
func TestResumeInterruptedWorkflow_NearHistorySizeLimit_DoesNotReset(t *testing.T) {
	desc := closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 30000)
	desc.WorkflowExecutionInfo.HistorySizeBytes = 49 * 1024 * 1024
	tc := &mockPauseTemporalClient{
		describeResp:  desc,
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrHistoryLimitExceeded)
	assert.False(t, tc.resetCalled)
}

// An operator/reconciler terminate is a normal interruption: reset-and-replay
// stays the precise recovery for it.
func TestResumeInterruptedWorkflow_UnrelatedTerminate_StillResets(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "old-run", 11),
		historyEvents: terminatedByServerHistory("Workflow stuck: activity task in Scheduled state"),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	newRunID, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	require.NoError(t, err)
	assert.Equal(t, "new-run", newRunID)
	assert.True(t, tc.resetCalled)
}
