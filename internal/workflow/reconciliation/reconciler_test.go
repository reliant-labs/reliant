// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/observability"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
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

	mu                sync.Mutex
	describeResponses map[string]mockDescribeResponse // keyed by workflowID
	terminateCalls    []string                        // workflow IDs that were terminated

	// Poller-gating / reset support
	taskQueueResponses    map[enums.TaskQueueType]*workflowservice.DescribeTaskQueueResponse
	describeTaskQueueErr  error
	describeTaskQueueCnt  int
	resetCalls            []*workflowservice.ResetWorkflowExecutionRequest
	resetErr              error
	historyEvents         []*historypb.HistoryEvent
	getWorkflowHistoryCnt int
}

func (m *mockReconcilerTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if resp, ok := m.describeResponses[workflowID]; ok {
		return resp.resp, resp.err
	}
	// Default: not found
	return nil, fmt.Errorf("workflow %s not found (NotFound)", workflowID)
}

func (m *mockReconcilerTemporalClient) TerminateWorkflow(
	_ context.Context, workflowID, _ string, _ string, _ ...interface{},
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminateCalls = append(m.terminateCalls, workflowID)
	return nil
}

func (m *mockReconcilerTemporalClient) DescribeTaskQueue(
	_ context.Context, _ string, tqType enums.TaskQueueType,
) (*workflowservice.DescribeTaskQueueResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.describeTaskQueueCnt++
	if m.describeTaskQueueErr != nil {
		return nil, m.describeTaskQueueErr
	}
	if resp, ok := m.taskQueueResponses[tqType]; ok {
		return resp, nil
	}
	return &workflowservice.DescribeTaskQueueResponse{}, nil
}

func (m *mockReconcilerTemporalClient) ResetWorkflowExecution(
	_ context.Context, req *workflowservice.ResetWorkflowExecutionRequest,
) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetCalls = append(m.resetCalls, req)
	if m.resetErr != nil {
		return nil, m.resetErr
	}
	return &workflowservice.ResetWorkflowExecutionResponse{RunId: "reset-run"}, nil
}

func (m *mockReconcilerTemporalClient) GetWorkflowHistory(
	_ context.Context, _ string, _ string, _ bool, _ enums.HistoryEventFilterType,
) client.HistoryEventIterator {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getWorkflowHistoryCnt++
	return &fakeHistoryIterator{events: m.historyEvents}
}

// setPollersActive flips the poller state for BOTH task queue types.
func (m *mockReconcilerTemporalClient) setPollersActive(active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.taskQueueResponses == nil {
		m.taskQueueResponses = map[enums.TaskQueueType]*workflowservice.DescribeTaskQueueResponse{}
	}
	m.taskQueueResponses[enums.TASK_QUEUE_TYPE_WORKFLOW] = makePollersResponse(active)
	m.taskQueueResponses[enums.TASK_QUEUE_TYPE_ACTIVITY] = makePollersResponse(active)
}

type fakeHistoryIterator struct {
	events []*historypb.HistoryEvent
	idx    int
}

func (f *fakeHistoryIterator) HasNext() bool { return f.idx < len(f.events) }

func (f *fakeHistoryIterator) Next() (*historypb.HistoryEvent, error) {
	if f.idx >= len(f.events) {
		return nil, fmt.Errorf("no more events")
	}
	e := f.events[f.idx]
	f.idx++
	return e, nil
}

// --- Mock DB repository ---
// Only implements the methods used by ReconcileWorkflow.

type mockRepo struct {
	db.Repository // embed to satisfy interface

	mu                sync.Mutex
	updatedStatuses   map[string]db.WorkflowStatus // workflowID -> new status
	chats             map[string]*db.Chat          // chatID -> chat
	userUpdates       []*db.UserUpdate
	savedMessages     []savedMessage
	workflowsByStatus map[db.WorkflowStatus][]*db.Workflow

	// Progress-watchdog exclusion state
	pendingQuestions map[string]*db.Question   // chatID -> pending question
	pendingApprovals map[string][]*db.Approval // chatID -> pending approvals
	questionErr      error                     // forced error for GetPendingQuestionByChatID
	workflowsByChat  map[string][]*db.Workflow // chatID -> all workflow rows (paused-row exclusion)

	// Orphaned-descendant reap: rows the repair claims to have repaired, a
	// forced error, and how many times the pass invoked it. callOrder records
	// the repo calls a pass makes, in order, so a test can assert the reap
	// happens BEFORE the pass lists the workflows it will adjudicate.
	reapRows  int64
	reapErr   error
	reapCalls int
	callOrder []string

	// Orphaned-thread reap: same shape as the workflow-descendant reap above,
	// for ReapOrphanedThreads.
	reapThreadsRows  int64
	reapThreadsErr   error
	reapThreadsCalls int

	strandedSpawns    []*db.ToolCall
	strandedSpawnsErr error

	strandedBackgroundSpawns     []*db.StrandedBackgroundSpawn
	strandedBackgroundSpawnsErr  error
	enqueuedAgentMessages        []*db.AgentMessage
	enqueueAgentMessageInsertsFn func(*db.AgentMessage) (bool, error)

	// Threads the repair's recipient-liveness check can see, and the
	// tool_calls rows it writes through db.UpsertToolCallStatus. A thread
	// absent from the map reads as "cannot be loaded", matching the real
	// store's sql.ErrNoRows.
	threads           map[string]*db.Thread
	threadErr         error
	toolCalls         map[string]*db.ToolCall
	upsertedToolCalls []*db.ToolCall
	upsertToolCallErr error

	// Orphaned-mailbox sweep: the threads the query reports as terminal-with-
	// queued-rows, how many rows each resolve moves, and the threads actually
	// resolved (so a test can assert a live thread's queue was never touched).
	orphanedMailboxThreads    []string
	orphanedMailboxErr        error
	orphanedMailboxRows       map[string]int64
	orphanedMailboxResolveErr error
	resolvedMailboxThreads    []string
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
		pendingQuestions:  make(map[string]*db.Question),
		pendingApprovals:  make(map[string][]*db.Approval),
		workflowsByChat:   make(map[string][]*db.Workflow),
		threads:           make(map[string]*db.Thread),
		toolCalls:         make(map[string]*db.ToolCall),
	}
}

// withThread registers a thread the recipient-liveness check can read.
func (m *mockRepo) withThread(id string, status int32) *mockRepo {
	m.threads[id] = &db.Thread{ID: id, Status: status}
	return m
}

// withBackgroundedToolCall seeds a spawn call at status 6, the state a
// dispatched background spawn is left in.
func (m *mockRepo) withBackgroundedToolCall(id, chatID string) *mockRepo {
	m.toolCalls[id] = &db.ToolCall{
		ID:       id,
		ChatID:   chatID,
		ToolName: "spawn",
		Status:   core.ToolCallStatusBackgrounded,
	}
	return m
}

func (m *mockRepo) ListWorkflowsByChat(_ context.Context, chatID string) ([]*db.Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workflowsByChat[chatID], nil
}

func (m *mockRepo) GetPendingQuestionByChatID(_ context.Context, chatID string) (*db.Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.questionErr != nil {
		return nil, m.questionErr
	}
	return m.pendingQuestions[chatID], nil
}

func (m *mockRepo) ListPendingApprovalsByChat(_ context.Context, chatID string) ([]*db.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingApprovals[chatID], nil
}

func (m *mockRepo) CompareAndSwapWorkflowStatus(_ context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedStatuses[id] = newStatus
	return true, nil
}

func (m *mockRepo) UpdateWorkflowStatus(_ context.Context, id string, status db.WorkflowStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	m.callOrder = append(m.callOrder, "list-"+status.Label())
	out := m.workflowsByStatus[status]
	m.mu.Unlock()
	return out, nil
}

func (m *mockRepo) ReapOrphanedWorkflowDescendants(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapCalls++
	m.callOrder = append(m.callOrder, "reap")
	return m.reapRows, m.reapErr
}

func (m *mockRepo) ReapOrphanedThreads(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapThreadsCalls++
	m.callOrder = append(m.callOrder, "reap-threads")
	return m.reapThreadsRows, m.reapThreadsErr
}

// The embedded db.Repository is a nil interface, so every method the pass calls
// must be implemented here or it panics. The stranded-spawn repair runs on
// every pass; the default is "nothing stranded".
func (m *mockRepo) ListStrandedSpawnToolCalls(_ context.Context) ([]*db.ToolCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callOrder = append(m.callOrder, "repair_stranded_spawns")
	return m.strandedSpawns, m.strandedSpawnsErr
}

// Same rule as ListStrandedSpawnToolCalls above: the background-spawn repair
// also runs on every pass, so it needs an always-present mock method too.
func (m *mockRepo) ListStrandedBackgroundSpawnToolCalls(_ context.Context) ([]*db.StrandedBackgroundSpawn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callOrder = append(m.callOrder, "repair_stranded_background_spawns")
	return m.strandedBackgroundSpawns, m.strandedBackgroundSpawnsErr
}

func (m *mockRepo) EnqueueAgentMessageIfAbsent(_ context.Context, msg *db.AgentMessage) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enqueueAgentMessageInsertsFn != nil {
		inserted, err := m.enqueueAgentMessageInsertsFn(msg)
		if inserted {
			m.enqueuedAgentMessages = append(m.enqueuedAgentMessages, msg)
		}
		return inserted, err
	}
	m.enqueuedAgentMessages = append(m.enqueuedAgentMessages, msg)
	return true, nil
}

func (m *mockRepo) ListThreadsWithOrphanedAgentMessages(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orphanedMailboxThreads, m.orphanedMailboxErr
}

// GetThread backs the background-spawn repair's recipient-liveness check. A
// thread this mock has never heard of returns an error, which is exactly what
// the Postgres store does for a missing row (sql.ErrNoRows out of QueryRow) --
// so the default, with no threads registered, exercises the fail-toward-live
// branch rather than a nil-return branch the real store cannot produce.
func (m *mockRepo) GetThread(_ context.Context, id string) (*db.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.threadErr != nil {
		return nil, m.threadErr
	}
	if t, ok := m.threads[id]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("thread %s not found", id)
}

// GetToolCall / UpsertToolCall back db.UpsertToolCallStatus, which the repair
// uses to move a stranded call off status 6 (backgrounded). Together they make
// the mock's tool_calls map behave like the real upsert for the one property
// the repair depends on: the persisted status after the write.
func (m *mockRepo) GetToolCall(_ context.Context, id string) (*db.ToolCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call, ok := m.toolCalls[id]; ok {
		return call, nil
	}
	return nil, fmt.Errorf("tool call %s not found", id)
}

func (m *mockRepo) UpsertToolCall(_ context.Context, call *db.ToolCall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertToolCallErr != nil {
		return m.upsertToolCallErr
	}
	m.toolCalls[call.ID] = call
	m.upsertedToolCalls = append(m.upsertedToolCalls, call)
	return nil
}

// GetContentBlockByToolCallID is resolveToolCallMessage's lookup. The repair
// never supplies a message id, so this is always consulted; "no block" is the
// honest answer for a mock with no messages and leaves message_id nil.
func (m *mockRepo) GetContentBlockByToolCallID(_ context.Context, toolCallID string) (*db.MessageContentBlock, error) {
	return nil, fmt.Errorf("no content block for tool call %s", toolCallID)
}

func (m *mockRepo) MarkQueuedAgentMessagesUndeliveredForThread(_ context.Context, toThreadID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orphanedMailboxResolveErr != nil {
		return 0, m.orphanedMailboxResolveErr
	}
	m.resolvedMailboxThreads = append(m.resolvedMailboxThreads, toThreadID)
	return m.orphanedMailboxRows[toThreadID], nil
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

// makePollersResponse returns a DescribeTaskQueue response with (or without)
// one recently-active poller.
func makePollersResponse(active bool) *workflowservice.DescribeTaskQueueResponse {
	if !active {
		return &workflowservice.DescribeTaskQueueResponse{}
	}
	return &workflowservice.DescribeTaskQueueResponse{
		Pollers: []*taskqueuepb.PollerInfo{
			{Identity: "worker-1", LastAccessTime: timestamppb.New(time.Now())},
		},
	}
}

// makeStuckActivityDescribeResp returns a running workflow whose pending
// activity has been sitting in Scheduled state for scheduledAgo.
func makeStuckActivityDescribeResp(runID, activityID string, scheduledAgo time.Duration) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{RunId: runID},
		},
		PendingActivities: []*workflowpb.PendingActivityInfo{
			{
				ActivityId:    activityID,
				ActivityType:  &commonpb.ActivityType{Name: "execute_step"},
				State:         enums.PENDING_ACTIVITY_STATE_SCHEDULED,
				ScheduledTime: timestamppb.New(time.Now().Add(-scheduledAgo)),
			},
		},
	}
}

// makeStuckWorkflowTaskDescribeResp returns a running workflow whose pending
// WORKFLOW task has been sitting in Scheduled state for scheduledAgo.
func makeStuckWorkflowTaskDescribeResp(runID string, scheduledAgo time.Duration) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{RunId: runID},
		},
		PendingWorkflowTask: &workflowpb.PendingWorkflowTaskInfo{
			State:         enums.PENDING_WORKFLOW_TASK_STATE_SCHEDULED,
			ScheduledTime: timestamppb.New(time.Now().Add(-scheduledAgo)),
		},
	}
}

// makeHistoryWithActivity returns a minimal history: workflow task completed
// at event 4, the given activity scheduled at event 5.
func makeHistoryWithActivity(activityID string) []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		{EventId: 1, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED},
		{EventId: 2, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED},
		{EventId: 3, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_STARTED},
		{EventId: 4, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED},
		{
			EventId:   5,
			EventType: enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
				ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
					ActivityId: activityID,
				},
			},
		},
	}
}

// stuckTestConfig returns a config whose debounce confirms after `passes`
// consecutive poller-active observations (time window effectively disabled).
func stuckTestConfig(passes int) *ReconcilerConfig {
	return &ReconcilerConfig{
		StuckConfirmationPasses: passes,
		StuckConfirmationWindow: time.Nanosecond,
		Namespace:               "test-ns",
	}
}

func runningWorkflow() *db.Workflow {
	return &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Thread:       "main",
		Status:       db.Active(),
		WorkflowName: "builtin://agent",
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
		Status:       db.Paused(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+missing should be detected")
	assert.Equal(t, db.Completed(), result.TemporalStatus)
	assert.Equal(t, db.Completed(), repo.updatedStatuses["wf-1"], "should repair DB status")
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
		Status:       db.Paused(),
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
		Status:       db.Paused(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+terminal should be detected")
	assert.Equal(t, db.Failed(), result.TemporalStatus)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
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
		Status:       db.Paused(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale, "paused+terminal should be detected")
	assert.Equal(t, db.Completed(), result.TemporalStatus)
	assert.Equal(t, db.Completed(), repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
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
		Status:       db.Active(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.True(t, result.NeedsRecovery)
	assert.Equal(t, db.Completed(), result.TemporalStatus)
	assert.Equal(t, db.Completed(), repo.updatedStatuses["wf-1"], "should repair DB status to completed")
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
		Status:       db.Active(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, db.Failed(), result.TemporalStatus)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"], "should repair DB status to match Temporal")
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
		Status:       db.Active(),
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
		Status:       db.Completed(),
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
		Status:       db.Active(),
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
		Status:       db.Active(),
		WorkflowName: "builtin://agent",
	}

	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "running+running = no mismatch")
	assert.Empty(t, repo.updatedStatuses)
}

// --- Stuck-task handling: poller gating, debounce, reset-first recovery ---

func TestReconciler_StuckActivity_NoPollers_Skipped(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(false) // worker down/rebuilding

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	// Many passes: with no pollers, stuck handling must never trigger.
	for i := 0; i < 5; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: no action while pollers absent", i)
		assert.False(t, result.RecoveredByReset, "pass %d", i)
	}

	assert.Empty(t, tempClient.resetCalls, "must not reset while pollers absent")
	assert.Empty(t, tempClient.terminateCalls, "must not terminate while pollers absent")
	assert.Empty(t, repo.updatedStatuses, "must not touch DB while pollers absent")
	assert.Empty(t, repo.savedMessages, "must not post chat messages while pollers absent")
}

func TestReconciler_StuckActivity_PollerCheckError_Skipped(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		describeTaskQueueErr: fmt.Errorf("temporal unavailable"),
	}

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 4; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: no action when poller liveness unknown", i)
	}

	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
}

func TestReconciler_StuckActivity_StalePollers_Skipped(t *testing.T) {
	// Temporal keeps dead workers in the poller list for ~5 minutes; only a
	// RECENTLY active poller counts.
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		taskQueueResponses: map[enums.TaskQueueType]*workflowservice.DescribeTaskQueueResponse{
			enums.TASK_QUEUE_TYPE_WORKFLOW: {Pollers: []*taskqueuepb.PollerInfo{
				{Identity: "dead-worker", LastAccessTime: timestamppb.New(time.Now().Add(-4 * time.Minute))},
			}},
			enums.TASK_QUEUE_TYPE_ACTIVITY: {Pollers: []*taskqueuepb.PollerInfo{
				{Identity: "dead-worker", LastAccessTime: timestamppb.New(time.Now().Add(-4 * time.Minute))},
			}},
		},
	}

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 4; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: stale pollers must not count as active", i)
	}

	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, tempClient.terminateCalls)
}

func TestReconciler_StuckActivity_DebounceRequiresConsecutivePasses(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))
	wf := runningWorkflow()

	// Passes 1 and 2: observed but not confirmed.
	for i := 1; i <= 2; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.RecoveredByReset, "pass %d must not act yet", i)
		assert.Empty(t, tempClient.resetCalls, "pass %d must not reset yet", i)
		assert.Empty(t, tempClient.terminateCalls, "pass %d must not terminate yet", i)
	}

	// Pass 3: confirmed -> reset (not terminate).
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.RecoveredByReset, "third consecutive poller-active pass confirms")
	require.Len(t, tempClient.resetCalls, 1)
	assert.Empty(t, tempClient.terminateCalls, "reset succeeded - terminate must not run")
	assert.Empty(t, repo.updatedStatuses, "reset succeeded - DB status untouched")
	assert.Empty(t, repo.savedMessages, "reset succeeded - no error message posted")

	// The reset request targets the last WorkflowTaskCompleted before the
	// stuck activity was scheduled.
	req := tempClient.resetCalls[0]
	assert.Equal(t, "test-ns", req.Namespace)
	assert.Equal(t, "wf-1", req.WorkflowExecution.GetWorkflowId())
	assert.Equal(t, "run-1", req.WorkflowExecution.GetRunId())
	assert.Equal(t, int64(4), req.WorkflowTaskFinishEventId)
	assert.Contains(t, req.Reason, "recovering lost task")
}

func TestReconciler_StuckActivity_PollerOutageResetsDebounce(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))
	wf := runningWorkflow()

	// Two poller-active passes...
	tempClient.setPollersActive(true)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	// ...then a pass with pollers gone (worker restarting) resets the count...
	tempClient.setPollersActive(false)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	// ...so two more active passes still aren't enough...
	tempClient.setPollersActive(true)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.False(t, result.RecoveredByReset)
	assert.Empty(t, tempClient.resetCalls, "outage must reset the consecutive-pass count")

	// ...and the third consecutive active pass confirms.
	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.True(t, result.RecoveredByReset)
	assert.Len(t, tempClient.resetCalls, 1)
}

func TestReconciler_StuckActivity_ConfirmationWindowMustElapse(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	// Passes threshold is low, but the wall-clock window is 1h: no action.
	config := &ReconcilerConfig{
		StuckConfirmationPasses: 2,
		StuckConfirmationWindow: time.Hour,
	}
	reconciler := NewReconciler(repo, tempClient, config)
	wf := runningWorkflow()

	for i := 0; i < 5; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.RecoveredByReset, "pass %d: window not elapsed", i)
	}
	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, tempClient.terminateCalls)
}

func TestReconciler_StuckActivity_TerminateFallbackOnResetError(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
		resetErr:      fmt.Errorf("reset not allowed"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	// First pass observes; second confirms, reset fails, terminate fallback.
	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.False(t, result.RecoveredByReset)
	assert.True(t, result.WasStale)
	assert.Equal(t, db.Failed(), result.TemporalStatus)

	require.Len(t, tempClient.resetCalls, 1, "reset must be attempted BEFORE terminate")
	require.Len(t, tempClient.terminateCalls, 1, "terminate is the fallback after reset failure")
	assert.Equal(t, "wf-1", tempClient.terminateCalls[0])
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	require.Len(t, repo.savedMessages, 1)
	assert.Contains(t, repo.savedMessages[0].content, "automatic recovery was attempted")
}

// A workflow that keeps re-sticking at the same point (history not growing) is
// reset only up to the bounded-guard limit; beyond it the reconciler stops
// resetting and terminates + marks failed (routing the next user message to the
// coarse restart), instead of resetting forever.
func TestReconciler_StuckActivity_ResetGuardBoundsRepeatedResets(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			// HistoryLength defaults to 0 and never grows → no forward progress.
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	// stuckTestConfig(2): the debounce confirms on the SECOND consecutive
	// poller-active pass. Two passes per confirmation is the reliable pattern
	// (the first sets firstObserved, the second confirms after real wall-clock
	// elapsed) — a single-pass confirmation is flaky against the 1ns test window.
	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	reconciler.SetResetGuard(v2workflow.NewResetAttemptGuard(2))
	wf := runningWorkflow()

	exhaustedBefore := anomalyCount(anomalyResetAttemptsExhausted)

	// confirmOnce runs the observe pass then the confirm pass, returning the
	// confirming pass's result. Each successful reset clears the observation, so
	// every confirmation restarts the two-pass debounce.
	confirmOnce := func() *ReconciliationResult {
		reconciler.ReconcileWorkflow(context.Background(), wf) // observe
		return reconciler.ReconcileWorkflow(context.Background(), wf)
	}

	// The guard allows 2 resets without progress.
	for i := 1; i <= 2; i++ {
		result := confirmOnce()
		require.NoError(t, result.Error)
		assert.True(t, result.RecoveredByReset, "confirmation %d should reset", i)
	}
	require.Len(t, tempClient.resetCalls, 2)
	assert.Empty(t, tempClient.terminateCalls, "no terminate while resets still allowed")

	// Third confirmation: guard exhausted → skip reset, terminate + mark failed.
	result := confirmOnce()
	require.NoError(t, result.Error)
	assert.False(t, result.RecoveredByReset)
	assert.Len(t, tempClient.resetCalls, 2, "no 3rd reset once the guard gives up")
	require.Len(t, tempClient.terminateCalls, 1, "bounded workflow is terminated")
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, exhaustedBefore+1, anomalyCount(anomalyResetAttemptsExhausted),
		"the reset_attempts_exhausted anomaly is recorded")
}

func TestReconciler_StuckWorkflowTask_ResetToLastWorkflowTaskCompleted(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckWorkflowTaskDescribeResp("run-1", 5*time.Minute)},
		},
		// No activity in flight - history ends after a completed workflow task.
		historyEvents: []*historypb.HistoryEvent{
			{EventId: 1, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED},
			{EventId: 2, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED},
			{EventId: 3, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_STARTED},
			{EventId: 4, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED},
			{EventId: 5, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.RecoveredByReset)
	require.Len(t, tempClient.resetCalls, 1)
	assert.Equal(t, int64(4), tempClient.resetCalls[0].WorkflowTaskFinishEventId)
	assert.Empty(t, tempClient.terminateCalls)
}

func TestReconciler_StuckActivity_ActivityIDChangeResetsDebounce(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-2"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))
	wf := runningWorkflow()

	// Two observations of act-1...
	reconciler.ReconcileWorkflow(context.Background(), wf)
	reconciler.ReconcileWorkflow(context.Background(), wf)

	// ...then the stuck activity changes (act-1 made progress): fresh window.
	tempClient.mu.Lock()
	tempClient.describeResponses["wf-1"] = mockDescribeResponse{
		resp: makeStuckActivityDescribeResp("run-1", "act-2", 5*time.Minute),
	}
	tempClient.mu.Unlock()

	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.False(t, result.RecoveredByReset, "identity change must restart the debounce")
	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.False(t, result.RecoveredByReset)
	assert.Empty(t, tempClient.resetCalls)

	// Third consecutive observation of act-2 confirms.
	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.True(t, result.RecoveredByReset)
	assert.Len(t, tempClient.resetCalls, 1)
}

func TestReconciler_PollerCheckCachedPerPass(t *testing.T) {
	repo := newMockRepo()
	wf1 := &db.Workflow{ID: "wf-1", ChatID: "chat-1", Status: db.Active(), WorkflowName: "builtin://agent"}
	wf2 := &db.Workflow{ID: "wf-2", ChatID: "chat-2", Status: db.Active(), WorkflowName: "builtin://agent"}
	repo.workflowsByStatus[db.Active()] = []*db.Workflow{wf1, wf2}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
			"wf-2": {resp: makeStuckActivityDescribeResp("run-2", "act-9", 5*time.Minute)},
		},
	}
	tempClient.setPollersActive(false)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	// One poller check per pass (one DescribeTaskQueue call per task queue
	// type), no matter how many workflows are stuck.
	assert.Equal(t, 2, tempClient.describeTaskQueueCnt,
		"expected exactly one workflow-queue + one activity-queue check per pass")
}

func TestReconciler_StuckObservationsPrunedWhenWorkflowGone(t *testing.T) {
	repo := newMockRepo()
	wf := runningWorkflow()
	repo.workflowsByStatus[db.Active()] = []*db.Workflow{wf}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(10))

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	reconciler.stuckMu.Lock()
	tracked := len(reconciler.stuckObservations)
	reconciler.stuckMu.Unlock()
	assert.Equal(t, 1, tracked, "stuck observation recorded")

	// Workflow no longer running -> its debounce entry is pruned.
	repo.workflowsByStatus[db.Active()] = nil
	_, errs = reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	reconciler.stuckMu.Lock()
	tracked = len(reconciler.stuckObservations)
	reconciler.stuckMu.Unlock()
	assert.Equal(t, 0, tracked, "entries for non-running workflows are pruned")
}

// --- Wedged workflow task handling: detect, debounce, terminate (no reset) ---

// makeWedgedWorkflowTaskDescribeResp returns a running workflow whose pending
// WORKFLOW task has the given attempt count (being dispatched and failing
// repeatedly — the non-determinism wedge class). State is STARTED with a fresh
// scheduled time: the task never sits in Scheduled long enough to look stuck.
func makeWedgedWorkflowTaskDescribeResp(runID string, attempt int32) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{RunId: runID},
		},
		PendingWorkflowTask: &workflowpb.PendingWorkflowTaskInfo{
			State:         enums.PENDING_WORKFLOW_TASK_STATE_STARTED,
			ScheduledTime: timestamppb.New(time.Now()),
			Attempt:       attempt,
		},
	}
}

func TestReconciler_WedgedWorkflowTask_TerminatedAndMarkedFailed(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	// First pass observes; second confirms.
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "first pass must only observe")
	assert.Empty(t, tempClient.terminateCalls, "first pass must not terminate")

	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, db.Failed(), result.TemporalStatus)

	// Reset is USELESS for the wedge class (replay re-diverges) — recovery
	// must terminate directly, never attempt a reset.
	assert.Empty(t, tempClient.resetCalls, "wedge recovery must NOT attempt a reset")
	require.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, "wf-1", tempClient.terminateCalls[0])

	// DB marked failed so the next SendMessage resumes at position.
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])

	// User told what happened and how to continue.
	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, "chat-1", repo.savedMessages[0].chatID)
	assert.Contains(t, repo.savedMessages[0].content, "will resume where it left off when you send a message")
}

func TestReconciler_WedgedWorkflowTask_PausedWorkflow_TerminatedAndMarkedFailed(t *testing.T) {
	// A PAUSED workflow whose replay wedges (deploy changed determinism while
	// it was parked) can never process its resume signal — it must be
	// terminated and marked failed exactly like a running wedge, or it burns
	// workflow-task retries forever and the chat is permanently dead.
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 471)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()
	wf.Status = db.Paused()

	// First pass observes; second confirms (the paused status must not clear
	// the wedge debounce streak between passes).
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.False(t, result.WasStale, "first pass must only observe")
	assert.Empty(t, tempClient.terminateCalls, "first pass must not terminate")

	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, db.Failed(), result.TemporalStatus)

	assert.Empty(t, tempClient.resetCalls, "wedge recovery must NOT attempt a reset")
	require.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	require.Len(t, repo.savedMessages, 1)
}

func TestReconciler_WedgedWorkflowTask_BelowThreshold_NoAction(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			// Attempt 3 < default threshold 5: normal transient retries.
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 3)},
		},
	}
	tempClient.setPollersActive(true)

	cfg := stuckTestConfig(1)
	reconciler := NewReconciler(repo, tempClient, cfg)
	wf := runningWorkflow()

	for i := 0; i < 4; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: below-threshold attempts are not a wedge", i)
	}
	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_WedgedWorkflowTask_NoPollers_Skipped(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(false) // worker down/rebuilding

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 5; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: no action while pollers absent", i)
	}
	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_WedgedWorkflowTask_DebounceRequiresConsecutivePasses(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 99)},
		},
	}

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))
	wf := runningWorkflow()

	// Two poller-active passes...
	tempClient.setPollersActive(true)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	// ...a poller outage resets the count...
	tempClient.setPollersActive(false)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	// ...so two more active passes still aren't enough...
	tempClient.setPollersActive(true)
	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.False(t, result.WasStale)
	assert.Empty(t, tempClient.terminateCalls, "outage must reset the consecutive-pass count")

	// ...and the third consecutive active pass confirms.
	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.True(t, result.WasStale)
	assert.Len(t, tempClient.terminateCalls, 1)
}

func TestReconciler_WedgedWorkflowTask_PrecedenceOverStuckReset(t *testing.T) {
	// A wedged workflow task can transiently sit in Scheduled state between
	// retries with an old-looking scheduled time. It must be classified as a
	// wedge (terminate), never as a lost task (reset) — resetting a
	// non-deterministic wedge just re-diverges.
	repo := newMockRepo()
	resp := makeStuckWorkflowTaskDescribeResp("run-1", 5*time.Minute)
	resp.PendingWorkflowTask.Attempt = 42
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: resp},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)

	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Empty(t, tempClient.resetCalls, "wedge must take precedence over stuck-in-Scheduled reset recovery")
	assert.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
}

func TestReconciler_WedgedThenRecovered_ClearsDebounce(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 10)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(3))
	wf := runningWorkflow()

	// Two wedge observations...
	reconciler.ReconcileWorkflow(context.Background(), wf)
	reconciler.ReconcileWorkflow(context.Background(), wf)

	// ...then the workflow makes progress (task succeeded): debounce clears.
	tempClient.mu.Lock()
	tempClient.describeResponses["wf-1"] = mockDescribeResponse{resp: makeRunningDescribeResp("run-1")}
	tempClient.mu.Unlock()
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.False(t, result.WasStale)

	reconciler.stuckMu.Lock()
	tracked := len(reconciler.stuckObservations)
	reconciler.stuckMu.Unlock()
	assert.Equal(t, 0, tracked, "recovered workflow must clear its wedge debounce entry")
	assert.Empty(t, tempClient.terminateCalls)
}

// --- Progress watchdog: cause-agnostic stall detection ---

// makeQuiescentDescribeResp returns a RUNNING workflow with ZERO pending work
// (no pending workflow task / activities / children / nexus ops) and the
// given HistoryLength — the progress watchdog's suspicious shape.
func makeQuiescentDescribeResp(runID string, historyLength int64) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:        enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution:     &commonpb.WorkflowExecution{RunId: runID},
			HistoryLength: historyLength,
		},
	}
}

// makeStartedActivityDescribeResp returns a RUNNING workflow with one pending
// activity in STARTED state (a legitimately slow in-flight activity, e.g. a
// long LLM call). Not stuck (not Scheduled), and not quiescent.
func makeStartedActivityDescribeResp(runID string, historyLength int64) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:        enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution:     &commonpb.WorkflowExecution{RunId: runID},
			HistoryLength: historyLength,
		},
		PendingActivities: []*workflowpb.PendingActivityInfo{
			{
				ActivityId:    "act-slow",
				ActivityType:  &commonpb.ActivityType{Name: "call_llm"},
				State:         enums.PENDING_ACTIVITY_STATE_STARTED,
				ScheduledTime: timestamppb.New(time.Now().Add(-time.Hour)),
			},
		},
	}
}

// progressTestConfig returns a config whose progress watchdog detects after
// detectPasses quiescent passes (confirmation at double that) with the
// wall-clock window effectively disabled.
func progressTestConfig(detectPasses int) *ReconcilerConfig {
	return &ReconcilerConfig{
		ProgressStallPasses: detectPasses,
		ProgressStallWindow: time.Nanosecond,
		Namespace:           "test-ns",
	}
}

// anomalyCount reads the current value of the reconciler anomaly counter for
// a class. Counters are process-global, so tests assert on deltas.
func anomalyCount(class string) float64 {
	return promtestutil.ToFloat64(observability.ReconcilerAnomaliesTotal.WithLabelValues(class))
}

func (r *Reconciler) progressObservationCount() int {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	return len(r.progressObservations)
}

func TestReconciler_ProgressWatchdog_DetectThenConfirm(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	detectedBefore := anomalyCount("progress_stall_detected")
	confirmedBefore := anomalyCount("progress_stall_confirmed")

	// Detect at 2 passes, confirm at 4.
	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	// Passes 1-3: observe + detect, but never act.
	for i := 1; i <= 3; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: no action before confirmation", i)
		assert.False(t, result.ProgressStalled, "pass %d", i)
		assert.Empty(t, tempClient.terminateCalls, "pass %d must not terminate", i)
	}
	assert.Equal(t, detectedBefore+1, anomalyCount("progress_stall_detected"),
		"detection metric fires exactly once per streak")
	assert.Equal(t, confirmedBefore, anomalyCount("progress_stall_confirmed"),
		"confirmation metric must not fire before the confirmation window")

	// Pass 4: confirmed -> terminate + mark failed + resumable-truth message.
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.True(t, result.ProgressStalled)
	assert.Equal(t, db.Failed(), result.TemporalStatus)

	// Unknown cause: never attempt a reset, terminate directly.
	assert.Empty(t, tempClient.resetCalls, "stall recovery must NOT attempt a reset")
	require.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, "wf-1", tempClient.terminateCalls[0])

	// DB marked failed so the next SendMessage resumes at position.
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])

	// User told what happened and how to continue.
	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, "chat-1", repo.savedMessages[0].chatID)
	assert.Contains(t, repo.savedMessages[0].content, "resume where it left off when you send a message")

	// Streak cleared and confirmation metric incremented exactly once.
	assert.Equal(t, 0, reconciler.progressObservationCount())
	assert.Equal(t, confirmedBefore+1, anomalyCount("progress_stall_confirmed"))
}

func TestReconciler_ProgressWatchdog_HistoryGrowthResetsStreak(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 10)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	// Three static passes (confirm would need 4)...
	for i := 0; i < 3; i++ {
		reconciler.ReconcileWorkflow(context.Background(), wf)
	}
	assert.Empty(t, tempClient.terminateCalls)

	// ...then history grows (= progress): the streak must restart.
	tempClient.mu.Lock()
	tempClient.describeResponses["wf-1"] = mockDescribeResponse{resp: makeQuiescentDescribeResp("run-1", 11)}
	tempClient.mu.Unlock()

	// Three more static passes at the new length still aren't enough...
	for i := 0; i < 3; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.ProgressStalled, "post-growth pass %d must not act", i)
	}
	assert.Empty(t, tempClient.terminateCalls, "history growth must reset the streak")

	// ...and the fourth consecutive static pass confirms.
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	assert.True(t, result.ProgressStalled)
	assert.Len(t, tempClient.terminateCalls, 1)
}

func TestReconciler_ProgressWatchdog_PendingQuestionExcluded(t *testing.T) {
	// A workflow parked on signal.question.<id> (ask_question / ask_user) has
	// the same Temporal footprint as a stall. The pending questions row is the
	// discriminator: it must suppress detection AND action entirely.
	repo := newMockRepo()
	repo.pendingQuestions["chat-1"] = &db.Question{
		ID:     "q-1",
		ChatID: "chat-1",
		Status: db.QuestionStatusPending,
	}
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	detectedBefore := anomalyCount("progress_stall_detected")
	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: question wait is not a stall", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
	assert.Equal(t, detectedBefore, anomalyCount("progress_stall_detected"),
		"signal-parked question wait must not even be reported")
	assert.Equal(t, 0, reconciler.progressObservationCount(),
		"exclusion at a threshold crossing clears the streak")
}

func TestReconciler_ProgressWatchdog_PendingApprovalExcluded(t *testing.T) {
	// Same as the question case, for tool approvals parked on
	// signal.approval.<id> with a pending approvals row.
	repo := newMockRepo()
	repo.pendingApprovals["chat-1"] = []*db.Approval{{}}
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: approval wait is not a stall", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_ProgressWatchdog_PausedDescendantExcluded(t *testing.T) {
	// A nested self-pause (retry exhaustion inside a spawned inline workflow)
	// parks the whole execution. The pause writer marks paused status
	// chat-wide, but if only nested rows got it and the root stayed running
	// (the invariant violation behind the 429-pause-terminated incident), the
	// paused descendant row must still keep the watchdog from terminating a
	// legitimately parked workflow.
	repo := newMockRepo()
	parentID := "wf-1"
	repo.workflowsByChat["chat-1"] = []*db.Workflow{
		{ID: "wf-1", ChatID: "chat-1", Status: db.Active()},
		{ID: "wf-spawn", ChatID: "chat-1", ParentID: &parentID, Status: db.Paused()},
	}
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	detectedBefore := anomalyCount("progress_stall_detected")
	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: pause-parked workflow is not a stall", i)
	}

	assert.Empty(t, tempClient.terminateCalls, "must never terminate a pause-parked workflow")
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
	assert.Equal(t, detectedBefore, anomalyCount("progress_stall_detected"),
		"pause-parked workflow must not even be reported as a stall")
	assert.Equal(t, 0, reconciler.progressObservationCount(),
		"exclusion at a threshold crossing clears the streak")
}

func TestReconciler_ProgressWatchdog_ExclusionCheckError_Holds(t *testing.T) {
	// If the wait-marker check fails, the watchdog must neither report nor
	// act (unknown exclusion state), and must not clear the streak.
	repo := newMockRepo()
	repo.questionErr = fmt.Errorf("db unavailable")
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	detectedBefore := anomalyCount("progress_stall_detected")
	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: unknown exclusion state must hold", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Equal(t, detectedBefore, anomalyCount("progress_stall_detected"))
	assert.Equal(t, 1, reconciler.progressObservationCount(),
		"streak is frozen, not cleared, while the exclusion check errors")
}

func TestReconciler_ProgressWatchdog_PendingActivityNotSuspicious(t *testing.T) {
	// A long in-flight activity (e.g. a slow LLM call) is pending work: the
	// workflow is NOT quiescent and must never accumulate a stall streak.
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStartedActivityDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Equal(t, 0, reconciler.progressObservationCount(),
		"pending work must not accumulate a stall streak")
}

func TestReconciler_ProgressWatchdog_PausedWorkflowNeverEnters(t *testing.T) {
	// Paused chats (user pause, retry-exhaustion, daemon-offline breaker) are
	// the "user is NOT awaiting progress" state: the watchdog must not track
	// them even when their Temporal execution is quiescent.
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := &db.Workflow{
		ID:           "wf-1",
		ChatID:       "chat-1",
		Thread:       "main",
		Status:       db.Paused(),
		WorkflowName: "builtin://agent",
	}

	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: paused+running is intentional", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Equal(t, 0, reconciler.progressObservationCount(),
		"paused workflows must never accumulate a stall streak")
}

func TestReconciler_ProgressWatchdog_ConfirmationWindowMustElapse(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	// Pass threshold is tiny but the wall-clock window is 1h: nothing fires.
	config := &ReconcilerConfig{
		ProgressStallPasses: 2,
		ProgressStallWindow: time.Hour,
	}
	detectedBefore := anomalyCount("progress_stall_detected")
	reconciler := NewReconciler(repo, tempClient, config)
	wf := runningWorkflow()

	for i := 0; i < 8; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: window not elapsed", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Equal(t, detectedBefore, anomalyCount("progress_stall_detected"))
}

func TestReconciler_ProgressWatchdog_ConfirmActionGatedOnPollers(t *testing.T) {
	// The confirmed destructive action must hold while workflow pollers are
	// absent (whole-system outage = known cause), WITHOUT resetting the
	// streak — quiescent workflows need no worker to make progress, so the
	// static-history evidence stays valid.
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(false)

	reconciler := NewReconciler(repo, tempClient, progressTestConfig(2))
	wf := runningWorkflow()

	// Six passes, all past the confirmation threshold from pass 4 on: no
	// pollers, no action.
	for i := 0; i < 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.ProgressStalled, "pass %d: no action while pollers absent", i)
	}
	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)

	// Pollers return: the already-confirmed stall acts on the next pass.
	tempClient.setPollersActive(true)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.ProgressStalled)
	require.Len(t, tempClient.terminateCalls, 1)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
}

func TestReconciler_ProgressObservationsPrunedWhenWorkflowGone(t *testing.T) {
	repo := newMockRepo()
	wf := runningWorkflow()
	repo.workflowsByStatus[db.Active()] = []*db.Workflow{wf}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	// Generous thresholds: observe only, never report/act.
	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, 1, reconciler.progressObservationCount(), "streak recorded")

	// Workflow no longer running -> its streak entry is pruned.
	repo.workflowsByStatus[db.Active()] = nil
	_, errs = reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, 0, reconciler.progressObservationCount(),
		"entries for non-running workflows are pruned")
}

// --- Anomaly metric emission for the pre-existing recovery classes ---

func TestReconciler_AnomalyMetrics_Counters(t *testing.T) {
	t.Run("lost_workflow_repaired", func(t *testing.T) {
		before := anomalyCount("lost_workflow_repaired")
		repo := newMockRepo()
		tempClient := &mockReconcilerTemporalClient{
			describeResponses: map[string]mockDescribeResponse{}, // not found
		}
		reconciler := NewReconciler(repo, tempClient, DefaultConfig())

		result := reconciler.ReconcileWorkflow(context.Background(), runningWorkflow())
		require.NoError(t, result.Error)
		require.True(t, result.NeedsRecovery)
		assert.Equal(t, before+1, anomalyCount("lost_workflow_repaired"))
	})

	t.Run("wedge_terminated", func(t *testing.T) {
		before := anomalyCount("wedge_terminated")
		repo := newMockRepo()
		tempClient := &mockReconcilerTemporalClient{
			describeResponses: map[string]mockDescribeResponse{
				"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 42)},
			},
		}
		tempClient.setPollersActive(true)
		reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
		wf := runningWorkflow()

		reconciler.ReconcileWorkflow(context.Background(), wf)
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		require.True(t, result.WasStale)
		assert.Equal(t, before+1, anomalyCount("wedge_terminated"))
	})

	t.Run("stuck_reset", func(t *testing.T) {
		before := anomalyCount("stuck_reset")
		repo := newMockRepo()
		tempClient := &mockReconcilerTemporalClient{
			describeResponses: map[string]mockDescribeResponse{
				"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
			},
			historyEvents: makeHistoryWithActivity("act-1"),
		}
		tempClient.setPollersActive(true)
		reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
		wf := runningWorkflow()

		reconciler.ReconcileWorkflow(context.Background(), wf)
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		require.True(t, result.RecoveredByReset)
		assert.Equal(t, before+1, anomalyCount("stuck_reset"))
	})

	t.Run("reset_failed_terminated", func(t *testing.T) {
		before := anomalyCount("reset_failed_terminated")
		repo := newMockRepo()
		tempClient := &mockReconcilerTemporalClient{
			describeResponses: map[string]mockDescribeResponse{
				"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
			},
			historyEvents: makeHistoryWithActivity("act-1"),
			resetErr:      fmt.Errorf("reset not allowed"),
		}
		tempClient.setPollersActive(true)
		reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
		wf := runningWorkflow()

		reconciler.ReconcileWorkflow(context.Background(), wf)
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		require.True(t, result.WasStale)
		assert.Equal(t, before+1, anomalyCount("reset_failed_terminated"))
	})
}

func TestReconcileRunningWorkflows_IncludesPausedWedgedZombie(t *testing.T) {
	// The background pass must fetch PAUSED workflows too: reconcileWorkflow's
	// paused-specific repairs (stale-paused repair, paused-wedge terminate)
	// are unreachable otherwise. Regression for the dev zombies that retried
	// a wedged workflow task at attempt 450+ for days while paused.
	repo := newMockRepo()
	zombie := runningWorkflow()
	zombie.Status = db.Paused()
	repo.workflowsByStatus[db.Paused()] = []*db.Workflow{zombie}
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 471)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))

	// First pass observes, second confirms — both through the background
	// entry point, which must pick the paused row up each time.
	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)
	assert.Empty(t, tempClient.terminateCalls, "first pass must only observe")

	_, errs = reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	require.Len(t, tempClient.terminateCalls, 1, "paused wedged zombie must be terminated by the background pass")
	assert.Equal(t, "wf-1", tempClient.terminateCalls[0])
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
}
