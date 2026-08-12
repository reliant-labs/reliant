// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/stretchr/testify/assert"
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
