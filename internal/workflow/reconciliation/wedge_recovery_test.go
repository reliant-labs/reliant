// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"testing"

	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A wedge is decided by what the run's history RECORDS, not by the pending
// workflow task's attempt counter alone. The counter climbs the same way for a
// task that keeps TIMING OUT on an overloaded host (slow, not stuck — the
// 2026-09-29 false wedge) and for one that keeps FAILING because the code can
// no longer replay the history (stuck forever — chats 97654413 and 3f03dc31,
// 2026-10-09). Only the second is acted on, and it is acted on by default:
// until the run is terminated its chat reads "active" with nothing running.

// fakeWorkflowService serves GetWorkflowExecutionHistoryReverse from the
// mock's forward history, newest first, the way Temporal does.
type fakeWorkflowService struct {
	workflowservice.WorkflowServiceClient
	m *mockReconcilerTemporalClient
}

func (m *mockReconcilerTemporalClient) WorkflowService() workflowservice.WorkflowServiceClient {
	return fakeWorkflowService{m: m}
}

func (f fakeWorkflowService) GetWorkflowExecutionHistoryReverse(
	_ context.Context, _ *workflowservice.GetWorkflowExecutionHistoryReverseRequest, _ ...grpc.CallOption,
) (*workflowservice.GetWorkflowExecutionHistoryReverseResponse, error) {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	reversed := make([]*historypb.HistoryEvent, 0, len(f.m.historyEvents))
	for i := len(f.m.historyEvents) - 1; i >= 0; i-- {
		reversed = append(reversed, f.m.historyEvents[i])
	}
	return &workflowservice.GetWorkflowExecutionHistoryReverseResponse{
		History: &historypb.History{Events: reversed},
	}, nil
}

func historyEvent(id int64, eventType enums.EventType) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: id, EventType: eventType}
}

func failedWorkflowTask(id int64, cause enums.WorkflowTaskFailedCause) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enums.EVENT_TYPE_WORKFLOW_TASK_FAILED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskFailedEventAttributes{
			WorkflowTaskFailedEventAttributes: &historypb.WorkflowTaskFailedEventAttributes{Cause: cause},
		},
	}
}

// replayDivergedTail is the tail of chat 97654413's run: parked, then the
// user's message woke it and every workflow task since failed TMPRL1100.
func replayDivergedTail() []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		historyEvent(11142, enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		historyEvent(11143, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(11144, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(11145, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		failedWorkflowTask(11146, enums.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
		historyEvent(11147, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
	}
}

// timedOutTail is the 2026-09-29 shape: the tasks time out, none fails.
func timedOutTail() []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		historyEvent(40, enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		historyEvent(41, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(42, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(43, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		historyEvent(44, enums.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT),
	}
}

func wedgedClient(attempt int32, tail []*historypb.HistoryEvent) *mockReconcilerTemporalClient {
	c := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", attempt)},
		},
		historyEvents: tail,
		retryingWedge: true,
	}
	c.setPollersActive(true)
	return c
}

// shippedConfigWithFastDebounce is DefaultConfig — interventions OFF, as
// shipped — with the debounce shrunk so a test can confirm in two passes.
func shippedConfigWithFastDebounce() *ReconcilerConfig {
	cfg := DefaultConfig()
	cfg.StuckConfirmationPasses = 2
	cfg.StuckConfirmationWindow = 1
	cfg.Namespace = "test-ns"
	return cfg
}

func TestReconciler_ReplayDivergedRun_RecoveredWithShippedConfig(t *testing.T) {
	repo := newMockRepo()
	tempClient := wedgedClient(28, replayDivergedTail())
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	wf := runningWorkflow()

	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.Empty(t, tempClient.terminateCalls, "first pass only observes")

	result = reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls,
		"a run whose history no longer replays is stuck forever; it must be ended even with interventions off")
	assert.Empty(t, tempClient.resetCalls, "a reset re-diverges on replay")
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"], "failed keeps the checkpoint, so the next message resumes at position")

	// The sub-agents ran inline in the dead execution. Left at running, they
	// keep the chat's activity at RUNNING — still "active" with nothing running.
	assert.Equal(t, []string{"wf-1"}, repo.cascadedDescendants)
	assert.Equal(t, []string{"wf-1"}, repo.cascadedThreadSubtrees)
	assert.Equal(t, db.StopReasonFailed, repo.cascadedReason)

	require.Len(t, repo.savedMessages, 1)
	assert.Contains(t, repo.savedMessages[0].content, "send a message")
}

// fakeQueuedDelivery records the chats it was asked to continue.
type fakeQueuedDelivery struct {
	continued []string
	start     bool
}

func (f *fakeQueuedDelivery) ContinueQueued(_ context.Context, chatID string) (bool, error) {
	f.continued = append(f.continued, chatID)
	return f.start, nil
}

func (f *fakeQueuedDelivery) Sweep(context.Context) (int, error) { return 0, nil }

// Chat 97654413: the user's "continue" was saved, and the run that took it in
// wedged and was ended. Ending it is not enough — the user already sent that
// message once — so the reconciler hands it to a fresh run at the checkpoint
// and says so, rather than asking for it again.
func TestReconciler_WedgedRunWithAnUndeliveredMessage_IsContinued(t *testing.T) {
	repo := newMockRepo()
	tempClient := wedgedClient(28, replayDivergedTail())
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	delivery := &fakeQueuedDelivery{start: true}
	reconciler.SetQueuedDelivery(delivery)
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)

	require.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
	assert.Equal(t, []string{wf.ChatID}, delivery.continued, "the wedged run's chat is continued once, after it is ended")
	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, wedgeContinuedChatMessage, repo.savedMessages[0].content,
		"the user is told the conversation continued, not asked to send again")
}

// With nothing undelivered (ContinueQueued starts nothing), the chat is told
// how to continue, as before.
func TestReconciler_WedgedRunWithNothingUndelivered_AsksForTheNextMessage(t *testing.T) {
	repo := newMockRepo()
	tempClient := wedgedClient(28, replayDivergedTail())
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	delivery := &fakeQueuedDelivery{start: false}
	reconciler.SetQueuedDelivery(delivery)
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)

	assert.Equal(t, []string{wf.ChatID}, delivery.continued)
	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, wedgeInterruptedChatMessage, repo.savedMessages[0].content)
}

// Right after the deploy that fixes a replay break, a run the fix heals still
// shows what the old workers recorded: a high attempt count and a TMPRL1100 in
// its history. Temporal backs the task off for minutes, so the fixed worker
// may not have retried it yet. Until a worker retries it and it fails again,
// the evidence is about the old code, and the run must be left to heal.
func TestReconciler_RecordedDivergenceNotYetRetried_IsLeftToHeal(t *testing.T) {
	repo := newMockRepo()
	tempClient := wedgedClient(28, replayDivergedTail())
	tempClient.retryingWedge = false // no worker has retried it since
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	wf := runningWorkflow()

	for i := 1; i <= 5; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d", i)
	}
	assert.Empty(t, tempClient.terminateCalls, "the fixed worker has not tried yet; terminating would throw away a run it heals")

	// The fixed worker's attempt fails too: now the current code is the one
	// that cannot replay it.
	tempClient.retryingWedge = true
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.True(t, result.WasStale)
	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
}

func TestReconciler_HighAttemptCountFromTimeouts_IsNotAWedge(t *testing.T) {
	repo := newMockRepo()
	tempClient := wedgedClient(42, timedOutTail())
	// Even with interventions ON: the history shows a slow worker, not a
	// wedge, and there is nothing a terminate would fix.
	reconciler := NewReconciler(repo, tempClient, stuckTestConfig(2))
	wf := runningWorkflow()

	for i := 1; i <= 5; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d", i)
	}
	assert.Empty(t, tempClient.terminateCalls, "the 2026-09-29 false wedge: a timing-out task is slow, not stuck")
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_WedgedRunWithPendingCancel_EndsCancelledAndSilent(t *testing.T) {
	// Chat 3f03dc31: archiving it requested a cancel, and the workflow task
	// that would have delivered the cancel wedged. The owner asked for the
	// run to stop, so it ends cancelled — no "send a message to continue".
	tail := []*historypb.HistoryEvent{
		historyEvent(26752, enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		historyEvent(26753, enums.EVENT_TYPE_WORKFLOW_EXECUTION_CANCEL_REQUESTED),
		historyEvent(26754, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(26755, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		failedWorkflowTask(26756, enums.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
	}
	repo := newMockRepo()
	tempClient := wedgedClient(29, tail)
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	wf := runningWorkflow()
	wf.Status = db.Paused()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)

	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints, "a cancelled run has no position to resume")
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_WorkflowCodePanicOnEveryTask_IsAWedge(t *testing.T) {
	tail := []*historypb.HistoryEvent{
		historyEvent(8, enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED),
		failedWorkflowTask(12, enums.WORKFLOW_TASK_FAILED_CAUSE_WORKFLOW_WORKER_UNHANDLED_FAILURE),
	}
	repo := newMockRepo()
	tempClient := wedgedClient(9, tail)
	reconciler := NewReconciler(repo, tempClient, shippedConfigWithFastDebounce())
	wf := runningWorkflow()

	reconciler.ReconcileWorkflow(context.Background(), wf)
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
}
