// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A run that ends without saying so — terminated by an operator, by Temporal's
// history cap, or failed where nothing reported it — leaves the user's last
// message unanswered. Chat 66a045ce (2026-10-10): its replay wedged, an
// operator terminated it by hand, and the reconciler repaired the status and
// told the user to send their "continue" a third time. The reconciler carries
// it on instead, as it does for a wedge it ends itself, and exactly once.

func terminatedRunClient(reason string) *mockReconcilerTemporalClient {
	return &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent(reason),
	}
}

func TestReconciler_RunTerminatedByOthers_ContinuesTheUnansweredMessageOnce(t *testing.T) {
	repo := newTerminationRepo(terminatedChatWorkflow())
	reconciler := NewReconciler(repo, terminatedRunClient("terminated by operator: replay wedge"), DefaultConfig())
	delivery := &fakeQueuedDelivery{start: true}
	reconciler.SetQueuedDelivery(delivery)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, db.Failed(), repo.rows["wf-1"].Status)
	assert.Equal(t, []string{"chat-1"}, delivery.continued, "the run's chat is continued after it is marked failed")

	updates := repo.errorUpdates()
	require.Len(t, updates, 1)
	assert.Equal(t, silentTerminationContinuedSummary, updates[0].data["error_summary"],
		"the user is not asked to send again: the message is already being answered")
	assert.Contains(t, updates[0].data["error_message"], "terminated by operator: replay wedge")

	require.Len(t, repo.savedMessages, 1)
	assert.Equal(t, "Recovered from an internal error; continuing from your last message.", repo.savedMessages[0].content)
	assert.Equal(t, "thread-main", repo.savedMessages[0].thread)

	// Every later pass finds the row already FAILED and does nothing more.
	for range 3 {
		_, errs = reconciler.ReconcileRunningWorkflows(context.Background())
		require.Empty(t, errs)
	}
	assert.Len(t, delivery.continued, 1, "continued exactly once")
	assert.Len(t, repo.errorUpdates(), 1)
	assert.Len(t, repo.savedMessages, 1)
}

// With nothing owed (ContinueQueued starts nothing), the user is told what
// happened and how to continue, as before.
func TestReconciler_RunTerminatedWithNothingOwed_AsksForTheNextMessage(t *testing.T) {
	repo := newTerminationRepo(terminatedChatWorkflow())
	reconciler := NewReconciler(repo, terminatedRunClient("terminated"), DefaultConfig())
	delivery := &fakeQueuedDelivery{start: false}
	reconciler.SetQueuedDelivery(delivery)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, []string{"chat-1"}, delivery.continued)
	updates := repo.errorUpdates()
	require.Len(t, updates, 1)
	assert.Equal(t, silentTerminationSummary, updates[0].data["error_summary"])
	assert.Empty(t, repo.savedMessages, "nothing was continued, so nothing says it was")
}

// A paused run waits for the user by definition; ending it is reported, but
// it is not started again on their behalf.
func TestReconciler_PausedRunEndedSilently_IsNotContinued(t *testing.T) {
	wf := terminatedChatWorkflow()
	wf.Status = db.Paused()
	repo := newTerminationRepo(wf)
	reconciler := NewReconciler(repo, terminatedRunClient("terminated"), DefaultConfig())
	delivery := &fakeQueuedDelivery{start: true}
	reconciler.SetQueuedDelivery(delivery)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, db.Failed(), repo.rows["wf-1"].Status)
	assert.Empty(t, delivery.continued)
	require.Len(t, repo.errorUpdates(), 1)
	assert.Equal(t, silentTerminationSummary, repo.errorUpdates()[0].data["error_summary"])
}

// The continuation reads the ended run's inputs with a query Temporal answers
// by replaying its whole history, so it must not inherit what is left of the
// pass's 30s budget: a run started on an expired context is no run at all.
func TestReconciler_ContinueAfterEnd_HasItsOwnDeadline(t *testing.T) {
	repo := newTerminationRepo(terminatedChatWorkflow())
	reconciler := NewReconciler(repo, terminatedRunClient("terminated"), DefaultConfig())
	delivery := &fakeQueuedDelivery{start: true}
	reconciler.SetQueuedDelivery(delivery)

	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, errs := reconciler.ReconcileRunningWorkflows(spent)
	require.Empty(t, errs)

	require.Len(t, delivery.ctxErrs, 1)
	assert.NoError(t, delivery.ctxErrs[0], "the continuation runs on its own budget")
}

// A run restarted at its checkpoint runs current code on an empty history, so
// it does not end the same way again — unless something ends it every time.
// The reconciler continues a chat on its own a bounded number of times, then
// leaves the next message to the user.
func TestReconciler_AutoContinue_IsBoundedPerChat(t *testing.T) {
	delivery := &fakeQueuedDelivery{start: true}
	tempClient := terminatedRunClient("terminated")
	reconciler := NewReconciler(newTerminationRepo(), tempClient, DefaultConfig())
	reconciler.SetQueuedDelivery(delivery)

	var summaries []any
	for range autoContinueLimit + 1 {
		// The continued run ends the same way: the row is running again.
		repo := newTerminationRepo(terminatedChatWorkflow())
		reconciler.repo = repo
		_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
		require.Empty(t, errs)
		require.Len(t, repo.errorUpdates(), 1)
		summaries = append(summaries, repo.errorUpdates()[0].data["error_summary"])
	}

	assert.Len(t, delivery.continued, autoContinueLimit, "continued automatically at most autoContinueLimit times")
	assert.Equal(t, silentTerminationSummary, summaries[autoContinueLimit],
		"past the limit the user is asked to send a message, and nothing claims it continued")

	// The bound is a window, not a lifetime.
	assert.True(t, reconciler.autoContinueAllowed("chat-1", time.Now().Add(autoContinueWindow+time.Minute)))
}
