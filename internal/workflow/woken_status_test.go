// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A resumed run with one short turn left can finish before the resume
// returns. Its COMPLETED is the truth, and the resume's own mark-running
// write must not overwrite it: it did, unconditionally, and the row read
// ACTIVE over a closed run — a chat that looked as if it were running forever
// (e2e story 05, TestStory05_DaemonOfflineCircuitBreakerPausesAndResumes,
// under load).
func TestResumeWorkflow_KeepsTheStatusTheWokenRunWrote(t *testing.T) {
	repo := newMockPauseRepo()
	tc := &mockPauseTemporalClient{}
	tc.afterSignal = func(signalName string) {
		if signalName == SignalResume {
			repo.updatedStatuses["wf-1"] = db.Completed() // the run finished
		}
	}
	ps := NewPauseService(tc, repo)

	require.NoError(t, ps.ResumeWorkflow(context.Background(), "wf-1", "chat-1", nil))

	assert.Equal(t, db.Completed(), repo.status("wf-1"))
}

// When the woken run has not written anything yet, the resume still marks it
// running at once — what a second send arriving next relies on.
func TestResumeWorkflow_MarksAStillPausedRunRunning(t *testing.T) {
	repo := newMockPauseRepo()
	ps := NewPauseService(&mockPauseTemporalClient{}, repo)

	require.NoError(t, ps.ResumeWorkflow(context.Background(), "wf-1", "chat-1", nil))

	assert.Equal(t, db.Active(), repo.status("wf-1"))
}

// A reset run replays to its reset point and goes on at once, so it can end
// before the resume marks it running.
func TestResumeInterruptedWorkflow_KeepsTheStatusTheResetRunWrote(t *testing.T) {
	repo := newMockPauseRepo()
	repo.initialStatus = db.Failed()
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	tc.afterSignal = func(string) { repo.updatedStatuses["wf-1"] = db.Completed() }
	ps := NewPauseService(tc, repo)

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1", nil)
	require.NoError(t, err)

	assert.Equal(t, db.Completed(), repo.status("wf-1"))
}
