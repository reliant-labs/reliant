// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Temporal client for PauseService tests ---

type mockPauseTemporalClient struct {
	client.Client // embed to satisfy the full interface

	signalErr    error
	signalCalls  []signalCall
	describeResp *workflowservice.DescribeWorkflowExecutionResponse
	describeErr  error

	historyEvents []*historypb.HistoryEvent
	resetResp     *workflowservice.ResetWorkflowExecutionResponse
	resetErr      error
	resetCalled   bool
	resetRequest  *workflowservice.ResetWorkflowExecutionRequest
}

type signalCall struct {
	workflowID string
	runID      string
	signalName string
}

func (m *mockPauseTemporalClient) SignalWorkflow(
	_ context.Context, workflowID, runID, signalName string, _ interface{},
) error {
	m.signalCalls = append(m.signalCalls, signalCall{
		workflowID: workflowID,
		runID:      runID,
		signalName: signalName,
	})
	// signalErr models the LIVE-execution signal (runID == ""). A signal to a
	// specific run (the post-reset re-signal) always succeeds — that run is fresh.
	if runID == "" {
		return m.signalErr
	}
	return nil
}

func (m *mockPauseTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return m.describeResp, m.describeErr
}

func (m *mockPauseTemporalClient) GetWorkflowHistory(
	_ context.Context, _ string, _ string, _ bool, _ enumspb.HistoryEventFilterType,
) client.HistoryEventIterator {
	return &mockHistoryIterator{events: m.historyEvents}
}

func (m *mockPauseTemporalClient) ResetWorkflowExecution(
	_ context.Context, req *workflowservice.ResetWorkflowExecutionRequest,
) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	m.resetCalled = true
	m.resetRequest = req
	return m.resetResp, m.resetErr
}

// closedDescribe builds a Describe response for a closed run with the given
// status, run ID, and history length.
func closedDescribe(status enumspb.WorkflowExecutionStatus, runID string, historyLen int64) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution:     &commonpb.WorkflowExecution{WorkflowId: "wf-1", RunId: runID},
			Status:        status,
			HistoryLength: historyLen,
		},
	}
}

// --- Mock DB repository for PauseService tests ---

type mockPauseRepo struct {
	db.Repository // embed to satisfy interface

	updatedStatuses map[string]db.WorkflowStatus
	updateErr       error
	cascadedStatus  db.WorkflowStatus
}

func newMockPauseRepo() *mockPauseRepo {
	return &mockPauseRepo{
		updatedStatuses: make(map[string]db.WorkflowStatus),
	}
}

func (m *mockPauseRepo) UpdateWorkflowStatus(_ context.Context, id string, status db.WorkflowStatus) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updatedStatuses[id] = status
	return nil
}

func (m *mockPauseRepo) CascadeTerminalStatusToDescendants(_ context.Context, _ string, status db.WorkflowStatus) error {
	m.cascadedStatus = status
	return nil
}

func (m *mockPauseRepo) PauseRunningWorkflowsByChat(_ context.Context, _ string) error {
	return nil
}

func (m *mockPauseRepo) ResumeWorkflowsByChat(_ context.Context, _ string) error {
	return nil
}

// =========================================================================
// isWorkflowAlreadyDoneErr tests
// =========================================================================

func TestIsWorkflowAlreadyDoneErr_AlreadyCompleted(t *testing.T) {
	err := fmt.Errorf("workflow execution already completed")
	assert.True(t, isWorkflowAlreadyDoneErr(err))
}

func TestIsWorkflowAlreadyDoneErr_NotFound(t *testing.T) {
	err := fmt.Errorf("workflow not found")
	assert.True(t, isWorkflowAlreadyDoneErr(err))
}

func TestIsWorkflowAlreadyDoneErr_NotFoundCamelCase(t *testing.T) {
	err := fmt.Errorf("rpc error: code = NotFound")
	assert.True(t, isWorkflowAlreadyDoneErr(err))
}

func TestIsWorkflowAlreadyDoneErr_WorkflowNotFound(t *testing.T) {
	err := fmt.Errorf("WorkflowNotFound: workflow wf-123 not available")
	assert.True(t, isWorkflowAlreadyDoneErr(err))
}

func TestIsWorkflowAlreadyDoneErr_GenuineError(t *testing.T) {
	err := fmt.Errorf("connection refused")
	assert.False(t, isWorkflowAlreadyDoneErr(err))
}

func TestIsWorkflowAlreadyDoneErr_TimeoutError(t *testing.T) {
	err := fmt.Errorf("context deadline exceeded")
	assert.False(t, isWorkflowAlreadyDoneErr(err))
}

// =========================================================================
// PauseWorkflow tests
// =========================================================================

func TestPauseWorkflow_Success(t *testing.T) {
	tc := &mockPauseTemporalClient{}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.NoError(t, err)

	// Should have sent signal.pause
	require.Len(t, tc.signalCalls, 1)
	assert.Equal(t, "wf-1", tc.signalCalls[0].workflowID)
	assert.Equal(t, SignalPause, tc.signalCalls[0].signalName)

	// DB should be updated to paused
	assert.Equal(t, db.WorkflowStatusPaused, repo.updatedStatuses["wf-1"])
}

func TestPauseWorkflow_AlreadyCompleted_ReconcilesAndReturnsNil(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr: fmt.Errorf("workflow execution already completed"),
		describeResp: &workflowservice.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
				Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			},
		},
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.NoError(t, err, "should return nil when workflow already completed")

	// DB should be reconciled to completed
	assert.Equal(t, db.WorkflowStatusCompleted, repo.updatedStatuses["wf-1"])
}

func TestPauseWorkflow_NotFound_ReconcilesAndReturnsNil(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr:   fmt.Errorf("workflow not found"),
		describeErr: fmt.Errorf("workflow not found"),
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.NoError(t, err, "should return nil when workflow not found")

	// When describe also fails, reconcile defaults to completed
	assert.Equal(t, db.WorkflowStatusCompleted, repo.updatedStatuses["wf-1"])
}

func TestPauseWorkflow_FailedWorkflow_ReconcilesCorrectStatus(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr: fmt.Errorf("workflow execution already completed"),
		describeResp: &workflowservice.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
				Status: enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
			},
		},
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.NoError(t, err)

	// Should reconcile to failed, not completed
	assert.Equal(t, db.WorkflowStatusFailed, repo.updatedStatuses["wf-1"])
}

func TestPauseWorkflow_CancelledWorkflow_ReconcilesCorrectStatus(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr: fmt.Errorf("workflow execution already completed"),
		describeResp: &workflowservice.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
				Status: enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED,
			},
		},
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.NoError(t, err)

	assert.Equal(t, db.WorkflowStatusCancelled, repo.updatedStatuses["wf-1"])
}

func TestPauseWorkflow_GenuineError_ReturnsError(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr: fmt.Errorf("connection refused"),
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send pause signal")
	assert.Contains(t, err.Error(), "connection refused")

	// DB should NOT be updated
	assert.Empty(t, repo.updatedStatuses)
}

func TestPauseWorkflow_DBUpdateFails_ReturnsError(t *testing.T) {
	tc := &mockPauseTemporalClient{} // signal succeeds
	repo := newMockPauseRepo()
	repo.updateErr = fmt.Errorf("database is locked")
	ps := NewPauseService(tc, repo)

	err := ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "user paused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update workflow status")
}

// =========================================================================
// ResumeInterruptedWorkflow tests (reset-and-replay decision)
// =========================================================================

// failedByActivityHistory: FAILED because activity A (scheduled at 5 by WFT#1 at
// 4) failed at 7. The reset point is event 4.
func failedByActivityHistory() []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeActivityScheduled(5),
		makeActivityFailed(7, 5),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(11, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED),
	}
}

func TestResumeInterruptedWorkflow_Failed_ResetsResumesAndMarksRunning(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	repo := newMockPauseRepo()
	ps := NewPauseService(tc, repo)
	ps.SetResetGuard(NewResetAttemptGuard(2))

	newRunID, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	require.NoError(t, err)
	assert.Equal(t, "new-run", newRunID)

	require.True(t, tc.resetCalled, "should reset the closed execution")
	assert.Equal(t, int64(4), tc.resetRequest.WorkflowTaskFinishEventId, "reset before the failing activity")

	// Resume signal sent to the NEW run.
	require.Len(t, tc.signalCalls, 1)
	assert.Equal(t, "new-run", tc.signalCalls[0].runID)
	assert.Equal(t, SignalResume, tc.signalCalls[0].signalName)

	assert.Equal(t, db.WorkflowStatusRunning, repo.updatedStatuses["wf-1"])
	assert.Equal(t, 1, ps.resetGuard.Attempts("wf-1"), "a reset attempt is recorded")
}

func TestResumeInterruptedWorkflow_NotFound_ReturnsNoReplayableHistory(t *testing.T) {
	tc := &mockPauseTemporalClient{describeErr: fmt.Errorf("workflow not found")}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrNoReplayableHistory)
	assert.False(t, tc.resetCalled)
}

func TestResumeInterruptedWorkflow_Running_NotEligible(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp: closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "run", 5),
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrNoReplayableHistory, "a running execution is not reset-resumed here")
	assert.False(t, tc.resetCalled)
}

func TestResumeInterruptedWorkflow_Cancelled_NotEligible(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp: closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, "run", 5),
	}
	ps := NewPauseService(tc, newMockPauseRepo())

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrNoReplayableHistory, "user-cancelled runs start fresh, not reset-resumed")
	assert.False(t, tc.resetCalled)
}

func TestResumeInterruptedWorkflow_GuardExhausted_FallsBack(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())
	guard := NewResetAttemptGuard(1)
	ps.SetResetGuard(guard)
	// Pre-exhaust the guard at the same history length (no progress).
	guard.Record("wf-1", 11)

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	assert.ErrorIs(t, err, ErrResetAttemptsExhausted)
	assert.False(t, tc.resetCalled, "no reset once the guard has given up")
}

// =========================================================================
// SignalWithRecovery — reset-and-replay of a closed (question/pause-parked) run
// =========================================================================

func TestSignalWithRecovery_LiveWorkflow_SignalsDirectly(t *testing.T) {
	tc := &mockPauseTemporalClient{} // no signalErr → live signal succeeds
	ps := NewPauseService(tc, newMockPauseRepo())

	err := ps.SignalWithRecovery(context.Background(), "wf-1", SignalResume, nil)
	require.NoError(t, err)
	require.Len(t, tc.signalCalls, 1)
	assert.Equal(t, "", tc.signalCalls[0].runID)
	assert.False(t, tc.resetCalled, "a live workflow is signaled directly, never reset")
}

func TestSignalWithRecovery_ClosedFailed_ResetsAndReSignalsOnNewRun(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr:     fmt.Errorf("workflow execution already completed"),
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())
	ps.SetResetGuard(NewResetAttemptGuard(2))

	// A question answer to a dead (Failed) question-parked run: reset-replay, then
	// re-deliver signal.question.<id> on the new run.
	err := ps.SignalWithRecovery(context.Background(), "wf-1", "signal.question.q1", map[string]interface{}{"status": "resolved"})
	require.NoError(t, err)

	require.True(t, tc.resetCalled, "closed run must be reset-and-replayed")
	require.Len(t, tc.signalCalls, 2, "initial live signal failed, re-signal delivered on the new run")
	assert.Equal(t, "", tc.signalCalls[0].runID)
	assert.Equal(t, "new-run", tc.signalCalls[1].runID)
	assert.Equal(t, "signal.question.q1", tc.signalCalls[1].signalName)
}

func TestSignalWithRecovery_ClosedFailed_GuardExhausted_FallsBack(t *testing.T) {
	tc := &mockPauseTemporalClient{
		signalErr:     fmt.Errorf("workflow execution already completed"),
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 11),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())
	g := NewResetAttemptGuard(1)
	ps.SetResetGuard(g)
	g.Record("wf-1", 11) // pre-exhaust at the same history length

	err := ps.SignalWithRecovery(context.Background(), "wf-1", "signal.question.q1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reset expired workflow",
		"guard-exhausted surfaces the legacy reset error so callers coarse-restart")
	assert.False(t, tc.resetCalled, "no reset once the guard has given up")
}

func TestResumeInterruptedWorkflow_ResetGuardProgress_AllowsAgain(t *testing.T) {
	tc := &mockPauseTemporalClient{
		describeResp:  closedDescribe(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "old-run", 400),
		historyEvents: failedByActivityHistory(),
		resetResp:     &workflowservice.ResetWorkflowExecutionResponse{RunId: "new-run"},
	}
	ps := NewPauseService(tc, newMockPauseRepo())
	guard := NewResetAttemptGuard(1)
	ps.SetResetGuard(guard)
	// Prior attempts recorded at a SMALLER history length; the current run made
	// forward progress (400 > 11), so a reset is allowed again.
	guard.Record("wf-1", 11)

	_, err := ps.ResumeInterruptedWorkflow(context.Background(), "wf-1", "chat-1")
	require.NoError(t, err)
	assert.True(t, tc.resetCalled)
	// Ensure the sentinel errors are distinct (defensive).
	assert.False(t, errors.Is(ErrResetAttemptsExhausted, ErrNoReplayableHistory))
}
