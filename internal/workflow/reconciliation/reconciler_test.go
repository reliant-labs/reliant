// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Config tests ---

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	assert.Equal(t, 30*time.Second, config.PollInterval)
}

func TestNewReconciler(t *testing.T) {
	reconciler := NewReconciler(nil, nil, nil)
	assert.NotNil(t, reconciler)
	assert.Equal(t, 30*time.Second, reconciler.pollInterval)
}

func TestNewReconcilerWithCustomConfig(t *testing.T) {
	config := &ReconcilerConfig{
		PollInterval: 10 * time.Second,
	}
	reconciler := NewReconciler(nil, nil, config)
	assert.NotNil(t, reconciler)
	assert.Equal(t, 10*time.Second, reconciler.pollInterval)
}

// --- Mock Temporal client for reconciler tests ---

type mockDescribeResponse struct {
	resp *workflowservice.DescribeWorkflowExecutionResponse
	err  error
}

type mockReconcilerTemporalClient struct {
	client.Client // embed to satisfy the full interface

	describeResponses map[string]mockDescribeResponse // keyed by workflowID
	terminateCalls    []string                        // workflow IDs that were terminated
}

func (m *mockReconcilerTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if resp, ok := m.describeResponses[workflowID]; ok {
		return resp.resp, resp.err
	}
	// Default: not found
	return nil, fmt.Errorf("workflow %s not found (NotFound)", workflowID)
}

func (m *mockReconcilerTemporalClient) TerminateWorkflow(
	_ context.Context, workflowID, _ string, _ string, _ ...interface{},
) error {
	m.terminateCalls = append(m.terminateCalls, workflowID)
	return nil
}

// --- Mock DB repository ---
// Only implements the methods used by ReconcileWorkflow.

type mockRepo struct {
	db.Repository // embed to satisfy interface

	updatedStatuses   map[string]db.WorkflowStatus // workflowID -> new status
	chats             map[string]*db.Chat          // chatID -> chat
	userUpdates       []*db.UserUpdate
	savedMessages     []savedMessage
	workflowsByStatus map[db.WorkflowStatus][]*db.Workflow
}

type savedMessage struct {
	chatID, thread, content string
	role                    int32
	workflowID              *string
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		updatedStatuses:   make(map[string]db.WorkflowStatus),
		chats:             make(map[string]*db.Chat),
		workflowsByStatus: make(map[db.WorkflowStatus][]*db.Workflow),
	}
}

func (m *mockRepo) CompareAndSwapWorkflowStatus(_ context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error) {
	m.updatedStatuses[id] = newStatus
	return true, nil
}

func (m *mockRepo) UpdateWorkflowStatus(_ context.Context, id string, status db.WorkflowStatus) error {
	m.updatedStatuses[id] = status
	return nil
}

func (m *mockRepo) GetChat(_ context.Context, id string) (*db.Chat, error) {
	if chat, ok := m.chats[id]; ok {
		return chat, nil
	}
	return nil, fmt.Errorf("chat not found: %s", id)
}

func (m *mockRepo) CreateUserUpdate(_ context.Context, update *db.UserUpdate) error {
	m.userUpdates = append(m.userUpdates, update)
	return nil
}

func (m *mockRepo) SaveMessageToThread(_ context.Context, chatID, thread string, role int32, content string, workflowID *string, _ []string, _ *int32) (*db.Message, error) {
	m.savedMessages = append(m.savedMessages, savedMessage{
		chatID:     chatID,
		thread:     thread,
		role:       role,
		content:    content,
		workflowID: workflowID,
	})
	return &db.Message{ID: "msg-1"}, nil
}

func (m *mockRepo) ListWorkflowsByStatus(_ context.Context, status db.WorkflowStatus) ([]*db.Workflow, error) {
	return m.workflowsByStatus[status], nil
}

// --- Helper to build DescribeWorkflowExecution responses ---

func makeRunningDescribeResp(runID string) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{
				RunId: runID,
			},
		},
	}
}

func makeTerminalDescribeResp(status enums.WorkflowExecutionStatus) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: status,
			Execution: &commonpb.WorkflowExecution{
				RunId: "some-run",
			},
		},
	}
}

// --- Reconciler repair tests ---

func TestReconciler_PausedWorkflow_TemporalDead_Repaired(t *testing.T) {
	repo := newMockRepo()

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusPaused,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+missing should be detected")
	assert.Equal(t, db.WorkflowStatusCompleted, result.TemporalStatus)
	assert.Equal(t, db.WorkflowStatusCompleted, repo.updatedStatuses["wf-1"], "should repair DB status")
}

func TestReconciler_PausedWorkflow_TemporalAlive_StaysPaused(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				resp: makeRunningDescribeResp("run-1"),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusPaused,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "should NOT detect stale state — paused+running is intentional")
	assert.Empty(t, repo.updatedStatuses, "should NOT update DB status")
	assert.Empty(t, repo.userUpdates, "should NOT emit user update")
}

func TestReconciler_PausedWorkflow_TemporalTimedOut_Repaired(t *testing.T) {
	repo := newMockRepo()

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusPaused,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+terminal should be detected")
	assert.Equal(t, db.WorkflowStatusFailed, result.TemporalStatus)
	assert.Equal(t, db.WorkflowStatusFailed, repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
}

func TestReconciler_PausedWorkflow_TemporalCompleted_Repaired(t *testing.T) {
	repo := newMockRepo()

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusPaused,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+terminal should be detected")
	assert.Equal(t, db.WorkflowStatusCompleted, result.TemporalStatus)
	assert.Equal(t, db.WorkflowStatusCompleted, repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
}

func TestReconciler_RunningWorkflow_TemporalDead_Repaired(t *testing.T) {
	repo := newMockRepo()
	repo.chats["chat-1"] = &db.Chat{
		ID:        "chat-1",
		UserID:    "user-1",
		ProjectID: "proj-1",
	}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			// No entry → not found
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.True(t, result.NeedsRecovery)
	assert.Equal(t, db.WorkflowStatusCompleted, result.TemporalStatus)
	assert.Equal(t, db.WorkflowStatusCompleted, repo.updatedStatuses["wf-1"], "should repair DB status to completed")
}

func TestReconciler_RunningWorkflow_TemporalFailed_Repaired(t *testing.T) {
	repo := newMockRepo()
	repo.chats["chat-1"] = &db.Chat{
		ID:        "chat-1",
		UserID:    "user-1",
		ProjectID: "proj-1",
	}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_FAILED),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, db.WorkflowStatusFailed, result.TemporalStatus)
	assert.Equal(t, db.WorkflowStatusFailed, repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
}

func TestReconciler_ChildWorkflow_Skipped(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	parentID := "parent-wf"
	wf := &db.Workflow{
		ID:           "child-wf",
		ParentID:     &parentID,
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "child workflows should be skipped")
	assert.Empty(t, repo.updatedStatuses, "should not touch DB for child workflows")
}

func TestReconciler_CompletedWorkflow_Skipped(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusCompleted,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "completed workflows should be skipped")
}

func TestReconciler_TemporalError_PropagatedAsError(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				err: fmt.Errorf("connection refused"),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "connection refused")
	assert.False(t, result.WasStale, "should not mark stale on Temporal error")
}

func TestReconciler_RunningWorkflow_TemporalRunning_NoChange(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {
				resp: makeRunningDescribeResp("run-1"),
			},
		},
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Status:       db.WorkflowStatusRunning,
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "running+running = no mismatch")
	assert.Empty(t, repo.updatedStatuses)
}
