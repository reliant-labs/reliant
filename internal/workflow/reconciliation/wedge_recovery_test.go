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

// fakeQueuedDelivery records the chats it was asked to continue, and whether
// the context it was handed was still live.
type fakeQueuedDelivery struct {
	continued []string
	ctxErrs   []error
	start     bool
}

func (f *fakeQueuedDelivery) ContinueQueued(ctx context.Context, chatID string) (bool, error) {
	f.continued = append(f.continued, chatID)
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
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
	assert.Equal(t, recoveredContinuingChatMessage, repo.savedMessages[0].content,
		"the user is told the conversation continued, not asked to send again")
}

// setPendingTask makes the next describe report the run's pending workflow
// task at the given attempt, and the history end with tail.
func (m *mockReconcilerTemporalClient) setPendingTask(attempt int32, tail []*historypb.HistoryEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.describeResponses["wf-1"] = mockDescribeResponse{resp: makeWedgedWorkflowTaskDescribeResp("run-1", attempt)}
	m.historyEvents = tail
}

// chat66a045ceTail is the tail of chat 66a045ce's run (2026-10-10) after the
// user's "continue" resumed it on a build that could not replay it: the first
// task failed TMPRL1100, the wake signals made the next one a recorded task,
// and that failed too. Every retry since is transient and leaves no event.
func chat66a045ceTail() []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		historyEvent(10305, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(10306, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(10307, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		failedWorkflowTask(10308, enums.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
		historyEvent(10309, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(10310, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(10312, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(10313, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
		failedWorkflowTask(10314, enums.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR),
	}
}

// Chat 66a045ce, 2026-10-10: the run's replay diverged at 14:42:19 UTC and
// Temporal retried it about once a minute (attempt 2 at 14:42:44, 3 at
// 14:43:44, 4 at 14:44:44). The reconciler, then waiting for attempt 5 and a
// 3-minute debounce, had not acted when an operator terminated the run by hand
// at 14:45:28. With the shipped config it must end the run on the first pass
// after a live worker fails the task again — here the 4th 30s pass, 1m39s
// after the first failure — and continue the user's message, once.
func TestReconciler_NondeterminismWedge_EndedOnTheFirstRepeatedFailure_WithShippedConfig(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}
	tempClient.setPollersActive(true)
	cfg := DefaultConfig() // as shipped; only the namespace is the test's
	cfg.Namespace = "test-ns"
	reconciler := NewReconciler(repo, tempClient, cfg)
	delivery := &fakeQueuedDelivery{start: true}
	reconciler.SetQueuedDelivery(delivery)
	wf := runningWorkflow()

	passes := []struct {
		at      string
		attempt int32
		tail    []*historypb.HistoryEvent
	}{
		// The first recorded failure is in; the recorded retry is in flight.
		{at: "14:42:28", attempt: 1, tail: chat66a045ceTail()[:8]},
		// The recorded retry failed too; the first transient retry
		// (attempt 2) failed at 14:42:44, attempt 3 is backing off.
		{at: "14:42:58", attempt: 3, tail: chat66a045ceTail()},
		{at: "14:43:28", attempt: 3, tail: chat66a045ceTail()},
		// Attempt 3 failed at 14:43:44 on the workers running now.
		{at: "14:43:58", attempt: 4, tail: chat66a045ceTail()},
	}
	for i, pass := range passes {
		tempClient.setPendingTask(pass.attempt, pass.tail)
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error, pass.at)
		if i < len(passes)-1 {
			require.Empty(t, tempClient.terminateCalls, "%s: no worker has failed the task again while watched yet", pass.at)
		}
	}

	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls, "ended at 14:43:58, 1m39s after the first failure")
	assert.Empty(t, tempClient.resetCalls, "a reset re-diverges on replay")
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, []string{wf.ChatID}, delivery.continued, "the user's unanswered \"continue\" is carried on, once")
	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, "Recovered from an internal error; continuing from your last message.", repo.savedMessages[0].content)

	// The run is FAILED now, so the next pass lists it no longer; were it
	// reconciled again, nothing more happens.
	wf.Status = db.Failed()
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)
	assert.Len(t, delivery.continued, 1)
	assert.Len(t, tempClient.terminateCalls, 1)
}

// The user sending again while the task fails makes the next task a recorded
// one at attempt 1, below the threshold. That is not the run recovering: the
// streak is kept, and the recorded task's own failure confirms it.
func TestReconciler_WedgeWhoseAttemptCountASignalRestarted_IsStillEnded(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}
	tempClient.setPollersActive(true)
	cfg := DefaultConfig()
	cfg.Namespace = "test-ns"
	reconciler := NewReconciler(repo, tempClient, cfg)
	wf := runningWorkflow()

	tempClient.setPendingTask(3, chat66a045ceTail())
	require.NoError(t, reconciler.ReconcileWorkflow(context.Background(), wf).Error)

	// "continue" again: a fresh recorded task, in flight.
	resent := append(chat66a045ceTail(),
		historyEvent(10315, enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED),
		historyEvent(10316, enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED),
		historyEvent(10317, enums.EVENT_TYPE_WORKFLOW_TASK_STARTED),
	)
	tempClient.setPendingTask(1, resent)
	require.NoError(t, reconciler.ReconcileWorkflow(context.Background(), wf).Error)
	require.Empty(t, tempClient.terminateCalls, "the fresh task has not failed yet")

	// It failed, as every task of this run does.
	tempClient.setPendingTask(2, append(resent, failedWorkflowTask(10318, enums.WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR)))
	require.NoError(t, reconciler.ReconcileWorkflow(context.Background(), wf).Error)
	assert.Equal(t, []string{"wf-1"}, tempClient.terminateCalls)
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
