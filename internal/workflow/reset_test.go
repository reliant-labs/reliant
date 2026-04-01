// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"fmt"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHistoryIterator implements client.HistoryEventIterator for testing.
type mockHistoryIterator struct {
	events []*historypb.HistoryEvent
	index  int
}

func (m *mockHistoryIterator) HasNext() bool {
	return m.index < len(m.events)
}

func (m *mockHistoryIterator) Next() (*historypb.HistoryEvent, error) {
	if m.index >= len(m.events) {
		return nil, fmt.Errorf("no more events")
	}
	event := m.events[m.index]
	m.index++
	return event, nil
}

// mockTemporalClient implements the subset of client.Client used by reset.go.
// We embed a nil client.Client to satisfy the full interface while only implementing
// the methods we need.
type mockTemporalClient struct {
	client.Client

	historyIter  client.HistoryEventIterator
	resetResp    *workflowservice.ResetWorkflowExecutionResponse
	resetErr     error
	resetCalled  bool
	resetRequest *workflowservice.ResetWorkflowExecutionRequest
}

func (m *mockTemporalClient) GetWorkflowHistory(
	_ context.Context, _ string, _ string, _ bool, _ enumspb.HistoryEventFilterType,
) client.HistoryEventIterator {
	return m.historyIter
}

func (m *mockTemporalClient) ResetWorkflowExecution(
	_ context.Context, req *workflowservice.ResetWorkflowExecutionRequest,
) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	m.resetCalled = true
	m.resetRequest = req
	return m.resetResp, m.resetErr
}

// Helper to create a history event with a given ID and type.
func makeEvent(eventID int64, eventType enumspb.EventType) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   eventID,
		EventType: eventType,
	}
}

// --- findLastWorkflowTaskCompleted tests ---

func TestFindLastWorkflowTaskCompleted_Normal(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(3, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(5, enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED),
		makeEvent(6, enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED),
		makeEvent(7, enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED),
		makeEvent(8, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(9, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
	}

	eventID, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, int64(10), eventID, "should return the last WorkflowTaskCompleted event ID")
}

func TestFindLastWorkflowTaskCompleted_SingleEvent(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(3, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
	}

	eventID, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, int64(4), eventID)
}

func TestFindLastWorkflowTaskCompleted_NoEvents(t *testing.T) {
	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: nil},
	}

	_, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no WorkflowTaskCompleted event found")
}

func TestFindLastWorkflowTaskCompleted_NoWorkflowTaskCompleted(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED),
		makeEvent(3, enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
	}

	_, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no WorkflowTaskCompleted event found")
}

func TestFindLastWorkflowTaskCompleted_MixedEvents(t *testing.T) {
	// Realistic workflow history: start → task cycle → activity → task cycle → signal → task cycle
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(3, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED), // first
		makeEvent(5, enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED),
		makeEvent(6, enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED),
		makeEvent(7, enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED),
		makeEvent(8, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(9, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(10, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED), // second
		makeEvent(11, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		makeEvent(12, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(13, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(14, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED), // third (last)
		makeEvent(15, enumspb.EVENT_TYPE_TIMER_STARTED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
	}

	eventID, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, int64(14), eventID, "should return event 14, the last WorkflowTaskCompleted")
}

func TestFindLastWorkflowTaskCompleted_IteratorError(t *testing.T) {
	// Iterator that returns an error on the second call
	iter := &errorIterator{
		events: []*historypb.HistoryEvent{
			makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		},
		errAtIndex: 1,
		err:        fmt.Errorf("connection lost"),
	}

	mc := &mockTemporalClient{
		historyIter: iter,
	}

	_, err := findLastWorkflowTaskCompleted(context.Background(), mc, "wf-1", "run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to iterate workflow history")
}

// errorIterator returns an error at a specific index.
type errorIterator struct {
	events     []*historypb.HistoryEvent
	index      int
	errAtIndex int
	err        error
}

func (e *errorIterator) HasNext() bool {
	return e.index <= e.errAtIndex && e.index < len(e.events)+1
}

func (e *errorIterator) Next() (*historypb.HistoryEvent, error) {
	if e.index == e.errAtIndex {
		e.index++
		return nil, e.err
	}
	if e.index >= len(e.events) {
		return nil, fmt.Errorf("no more events")
	}
	event := e.events[e.index]
	e.index++
	return event, nil
}

// --- ResetExpiredWorkflow tests ---

func TestResetExpiredWorkflow_Success(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		makeEvent(3, enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
		resetResp: &workflowservice.ResetWorkflowExecutionResponse{
			RunId: "new-run-id",
		},
	}

	newRunID, err := ResetExpiredWorkflow(context.Background(), mc, "wf-1", "old-run-id")
	require.NoError(t, err)
	assert.Equal(t, "new-run-id", newRunID)

	// Verify the reset request was correct
	require.True(t, mc.resetCalled)
	assert.Equal(t, "wf-1", mc.resetRequest.WorkflowExecution.WorkflowId)
	assert.Equal(t, "old-run-id", mc.resetRequest.WorkflowExecution.RunId)
	assert.Equal(t, int64(4), mc.resetRequest.WorkflowTaskFinishEventId)
	assert.Equal(t, TemporalNamespace, mc.resetRequest.Namespace)
	assert.Contains(t, mc.resetRequest.ResetReapplyExcludeTypes, enumspb.RESET_REAPPLY_EXCLUDE_TYPE_SIGNAL)
}

func TestResetExpiredWorkflow_NoHistory(t *testing.T) {
	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: nil},
	}

	_, err := ResetExpiredWorkflow(context.Background(), mc, "wf-1", "run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find reset point")
	assert.False(t, mc.resetCalled)
}

func TestResetExpiredWorkflow_ResetFails(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
		resetErr:    fmt.Errorf("workflow already terminated"),
	}

	_, err := ResetExpiredWorkflow(context.Background(), mc, "wf-1", "run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reset workflow execution")
}

func TestResetExpiredWorkflow_UsesCorrectResetPoint(t *testing.T) {
	// Verify it picks the LAST WorkflowTaskCompleted, not the first
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(3, enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED),
		makeEvent(4, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		makeEvent(5, enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED),
		makeEvent(6, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED), // last one
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
		resetResp: &workflowservice.ResetWorkflowExecutionResponse{
			RunId: "new-run",
		},
	}

	newRunID, err := ResetExpiredWorkflow(context.Background(), mc, "wf-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, "new-run", newRunID)
	assert.Equal(t, int64(6), mc.resetRequest.WorkflowTaskFinishEventId,
		"should reset to event 6, the last WorkflowTaskCompleted")
}

func TestResetExpiredWorkflow_SignalExcluded(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED),
		makeEvent(2, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
		resetResp: &workflowservice.ResetWorkflowExecutionResponse{
			RunId: "new-run",
		},
	}

	_, err := ResetExpiredWorkflow(context.Background(), mc, "wf-1", "run-1")
	require.NoError(t, err)
	// Verify RESET_REAPPLY_EXCLUDE_TYPE_SIGNAL is in the exclude list
	require.Len(t, mc.resetRequest.ResetReapplyExcludeTypes, 1)
	assert.Equal(t, enumspb.RESET_REAPPLY_EXCLUDE_TYPE_SIGNAL, mc.resetRequest.ResetReapplyExcludeTypes[0])
}

// Verify the request includes the WorkflowExecution with both workflow ID and run ID.
func TestResetExpiredWorkflow_IncludesWorkflowExecution(t *testing.T) {
	events := []*historypb.HistoryEvent{
		makeEvent(1, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
	}

	mc := &mockTemporalClient{
		historyIter: &mockHistoryIterator{events: events},
		resetResp: &workflowservice.ResetWorkflowExecutionResponse{
			RunId: "new-run",
		},
	}

	_, err := ResetExpiredWorkflow(context.Background(), mc, "my-workflow-id", "my-run-id")
	require.NoError(t, err)

	require.NotNil(t, mc.resetRequest.WorkflowExecution)
	assert.Equal(t, &commonpb.WorkflowExecution{
		WorkflowId: "my-workflow-id",
		RunId:      "my-run-id",
	}, mc.resetRequest.WorkflowExecution)
}
