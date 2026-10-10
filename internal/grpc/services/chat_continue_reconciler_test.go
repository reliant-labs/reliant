// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
)

// The reconciler's half of "a run that ended under the user's message carries
// on", end to end against the database: the reconciler finds the run ended in
// Temporal while the row still says running, and ContinueQueued — the same
// path a machine connect takes — starts the run that answers the message.
//
// Chat 66a045ce, 2026-10-10: its replay wedged, an operator terminated it, and
// the reconciler repaired the row and asked the user to send their "continue"
// a third time.

// terminatedRunTemporal is the fixture's ended run, closed by a TERMINATE.
type terminatedRunTemporal struct {
	*endedRunTemporalClient
}

func (terminatedRunTemporal) GetWorkflowHistory(context.Context, string, string, bool, enums.HistoryEventFilterType) client.HistoryEventIterator {
	return &closeEventIterator{event: &historypb.HistoryEvent{
		EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionTerminatedEventAttributes{
			WorkflowExecutionTerminatedEventAttributes: &historypb.WorkflowExecutionTerminatedEventAttributes{
				Reason: "replay wedge: terminated by operator",
			},
		},
	}}
}

type closeEventIterator struct {
	event *historypb.HistoryEvent
	done  bool
}

func (i *closeEventIterator) HasNext() bool { return !i.done }

func (i *closeEventIterator) Next() (*historypb.HistoryEvent, error) {
	if i.done {
		return nil, fmt.Errorf("no more events")
	}
	i.done = true
	return i.event, nil
}

// newTerminatedRunFixture is a chat whose run Temporal closed with a TERMINATE
// while its row still reads running, with the user's message unanswered. Its
// history no longer replays, so it is continued by a fresh run at the
// checkpoint.
func newTerminatedRunFixture(t *testing.T) (continueFixture, *reconciliation.Reconciler) {
	t.Helper()
	f := newContinueFixture(t, false)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Active()))
	f.temporal.status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED

	rec := reconciliation.NewReconciler(f.repo, terminatedRunTemporal{f.temporal}, reconciliation.DefaultConfig())
	rec.SetQueuedDelivery(queueddelivery.New(f.repo, onlineMachines{}, f.svc, f.temporal))
	return f, rec
}

func (f continueFixture) systemNotes(t *testing.T) []string {
	t.Helper()
	thread := f.threadID
	messages, err := f.repo.ListMessages(f.ctx, f.chatID, db.MessageListOptions{Thread: &thread})
	require.NoError(t, err)
	var notes []string
	for _, m := range messages {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		blocks, err := f.repo.ListContentBlocks(f.ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				notes = append(notes, *b.Content)
			}
		}
	}
	return notes
}

func TestReconciler_RunEndedByOthers_StartsTheRunThatAnswersTheMessage_Once(t *testing.T) {
	f, rec := newTerminatedRunFixture(t)

	_, errs := rec.ReconcileRunningWorkflows(f.ctx)
	require.Empty(t, errs)

	runsStarted := f.temporal.runsStarted()
	require.Len(t, runsStarted, 1, "the user's unanswered message gets its run without being sent again")
	assert.NotNil(t, runsStarted[0].input.Resume, "it continues from the checkpoint")
	wf, err := f.repo.GetWorkflow(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), wf.Status)
	assert.Contains(t, f.systemNotes(t), "Recovered from an internal error; continuing from your last message.")

	// The next passes find the continued run live, and leave it.
	for range 3 {
		_, errs = rec.ReconcileRunningWorkflows(f.ctx)
		require.Empty(t, errs)
	}
	assert.Len(t, f.temporal.runsStarted(), 1, "continued exactly once")
}

// A machine connect delivering the chat's message (queueddelivery) can race
// the reconciler's pass. Whichever gets there first starts the one run.
func TestReconciler_RunEndedByOthers_RacingADeliveryStartsOneRun(t *testing.T) {
	for i := range 5 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, rec := newTerminatedRunFixture(t)

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, errs := rec.ReconcileRunningWorkflows(f.ctx)
				assert.Empty(t, errs)
			}()
			go func() {
				defer wg.Done()
				_, err := f.svc.ContinueQueued(f.ctx, f.chatID)
				assert.NoError(t, err)
			}()
			wg.Wait()
			// A delivery that looked before the reconciler repaired the row
			// found the run "live" and left it; the next pass is the backstop.
			_, errs := rec.ReconcileRunningWorkflows(f.ctx)
			require.Empty(t, errs)

			assert.Len(t, f.temporal.runsStarted(), 1, "one run, never two")
		})
	}
}
