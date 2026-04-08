// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"fmt"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
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
	return m.signalErr
}

func (m *mockPauseTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return m.describeResp, m.describeErr
}

// --- Mock DB repository for PauseService tests ---

type mockPauseRepo struct {
	db.Repository // embed to satisfy interface

	updatedStatuses map[string]db.WorkflowStatus
	updateErr       error
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

func (m *mockPauseRepo) CompleteChildWorkflows(_ context.Context, _ string) error {
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
