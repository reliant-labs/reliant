package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestRecordAttentionDurableFailureAndIdentity(t *testing.T) {
	wantErr := errors.New("attention storage unavailable")
	calls := 0
	store := &MockWorkflowStatusStore{
		GetChatFunc: func(context.Context, string) (*db.Chat, error) {
			return &db.Chat{ID: "actual", LaunchKind: string(core.TriggerEventKindSchedule)}, nil
		},
		ApplyAttentionUpdateFunc: func(_ context.Context, u core.AttentionUpdate) (core.AttentionResult, error) {
			calls++
			require.Equal(t, "actual", u.ChatID)
			require.Equal(t, "finish", u.Phase)
			return core.AttentionResult{}, wantErr
		},
	}
	a := NewWorkflowStatusActivity(store)
	in := WorkflowStatusInput{ChatID: "actual", Status: "failed", LaunchRun: true, Attention: &core.AttentionUpdate{ChatID: "forged", Phase: "start"}}
	_, err := a.recordAttention(context.Background(), in)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, calls)
	in.LaunchRun = false
	result, err := a.recordAttention(context.Background(), in)
	require.NoError(t, err)
	require.False(t, result.Managed)
	require.Equal(t, 1, calls)
	in.ParentWorkflowID = "parent"
	_, err = a.recordAttention(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

type unavailableReceiptStore struct{ *db.Repo }

func (s unavailableReceiptStore) SetLaunchEventRunStatus(context.Context, string, string) error {
	return errors.New("receipt unavailable")
}

func TestAttentionReceiptFailureRollsBackCompletedLifecycle(t *testing.T) {
	f := newStreakFixture(t, false)
	ctx := context.Background()
	chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
	in := WorkflowStatusInput{ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: "completed", Outcome: "failure", LaunchRun: true, Thread: chatID,
		Attention: &core.AttentionUpdate{RunID: uuid.NewString(), StartedAt: time.Now().UTC(), Key: "incident", Phase: "finish", State: "action_required"}}
	var out WorkflowStatusOutput
	err := f.h.ExecuteActivity(NewWorkflowStatusActivity(unavailableReceiptStore{f.h.Repo()}).Execute, in, &out)
	require.Error(t, err)
	wf, err := f.h.Repo().GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	require.Equal(t, db.Active(), wf.Status, "cannot commit a completed lifecycle without its semantic fallback receipt")
	managed, err := f.h.Repo().AttentionManagedChats(ctx, f.userID, []string{chatID})
	require.NoError(t, err)
	require.False(t, managed[chatID])
}

type unavailableAttentionStore struct{ *db.Repo }

func (s unavailableAttentionStore) ApplyAttentionUpdate(context.Context, core.AttentionUpdate) (core.AttentionResult, error) {
	return core.AttentionResult{}, errors.New("attention storage unavailable")
}

func TestAttentionSuccessfulEscalationPersistsAtomically(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "lost-write"}[lost], func(t *testing.T) {
			f := newStreakFixture(t, false)
			ctx := context.Background()
			chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
			in := WorkflowStatusInput{ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: "completed", Outcome: "success", LaunchRun: true, Thread: chatID,
				Attention: &core.AttentionUpdate{RunID: uuid.NewString(), StartedAt: time.Now().UTC(), Key: "incident", Phase: "finish", State: "action_required"}}
			var store WorkflowStatusStore = f.h.Repo()
			if lost {
				store = unavailableAttentionStore{f.h.Repo()}
			}
			var out WorkflowStatusOutput
			err := f.h.ExecuteActivity(NewWorkflowStatusActivity(store).Execute, in, &out)
			wf, getErr := f.h.Repo().GetWorkflow(ctx, chatID)
			require.NoError(t, getErr)
			incidents, listErr := f.h.Repo().ListAttentionIncidents(ctx, f.userID)
			require.NoError(t, listErr)
			if lost {
				require.Error(t, err)
				require.Equal(t, db.Active(), wf.Status)
				require.Empty(t, incidents)
				return
			}
			require.NoError(t, err)
			require.Equal(t, db.Completed(), wf.Status)
			require.Len(t, incidents, 1)
			require.Equal(t, "action_required", incidents[0].State)
		})
	}
}

func TestAttentionTerminalPersistenceLossKeepsLegacyFailureVisible(t *testing.T) {
	f := newStreakFixture(t, false)
	ctx := context.Background()
	chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
	at := time.Now().UTC().Add(-time.Minute)
	actorID := uuid.NewString()
	_, err := f.h.Repo().ApplyAttentionUpdate(ctx, core.AttentionUpdate{ChatID: chatID, RunID: actorID, StartedAt: at, Phase: "start", Key: "observer"})
	require.NoError(t, err)
	in := WorkflowStatusInput{ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: "completed", Outcome: "failure", LaunchRun: true, Thread: chatID,
		Attention: &core.AttentionUpdate{RunID: actorID, StartedAt: at, Key: "observer", Phase: "finish", State: "action_required", FailureClass: "unsuccessful"}}
	var out WorkflowStatusOutput
	err = f.h.ExecuteActivity(NewWorkflowStatusActivity(unavailableAttentionStore{f.h.Repo()}).Execute, in, &out)
	require.Error(t, err, "terminal activity retries rather than acknowledge a lost transition")
	managed, err := f.h.Repo().AttentionManagedChats(ctx, f.userID, []string{chatID})
	require.NoError(t, err)
	require.False(t, managed[chatID], "a bare observer registration cannot permanently suppress failure")
	wf, err := f.h.Repo().GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	require.Equal(t, db.Active(), wf.Status, "attention failure rolls back the false completed lifecycle")
	// The runtime turns exhausted terminal persistence into a true Temporal
	// failure; the existing active-row reconciler then restores failed state.
	require.NoError(t, f.h.Repo().UpdateWorkflowStatus(ctx, chatID, db.Failed()))
	firings, err := f.h.Repo().FiringsSinceLastUnmanagedSuccess(ctx, f.userID, []string{f.triggerID}, 20)
	require.NoError(t, err)
	require.NotEmpty(t, firings[f.triggerID], "reconciled execution failure remains available to legacy inbox")
}

// Real status bookkeeping remains failed (including semantic failure routing),
// while a newer repair owner controls whether that incident pages its human.
func TestAttentionPreservesFailedOutcomeWhileRepairOwnsIncident(t *testing.T) {
	f := newStreakFixture(t, false)
	ctx := context.Background()
	observer := f.launch(core.TriggerEventKindSchedule, db.Active())
	repair := f.launch(core.TriggerEventKindWorkflowEvent, db.Active())
	at := time.Now().UTC().Add(-time.Minute)
	actorID := uuid.NewString()
	_, err := f.h.Repo().ApplyAttentionUpdate(ctx, core.AttentionUpdate{ChatID: observer, RunID: actorID, StartedAt: at, Phase: "start", Key: "repo:one:pr:12"})
	require.NoError(t, err)
	_, err = f.h.Repo().ApplyAttentionUpdate(ctx, core.AttentionUpdate{ChatID: repair, RunID: uuid.NewString(), StartedAt: at.Add(time.Second), Phase: "start", Key: "repo:one:pr:12", State: "recovering", RecoveryTimeout: time.Hour})
	require.NoError(t, err)
	// A late observer failure is fenced but remains an honest unsuccessful job.
	in := WorkflowStatusInput{ChatID: observer, WorkflowID: observer, WorkflowName: "builtin://agent", Status: "completed", Outcome: "failure", LaunchRun: true, Thread: observer,
		Attention: &core.AttentionUpdate{RunID: actorID, StartedAt: at, Key: "repo:one:pr:12", Phase: "finish", State: "action_required", FailureClass: "unsuccessful"}}
	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, in, &out))
	chat, err := f.h.Repo().GetChat(ctx, observer)
	require.NoError(t, err)
	require.False(t, chat.Unread)
	var status string
	require.NoError(t, f.h.DB().QueryRowContext(ctx, `SELECT run_status FROM trigger_events WHERE chat_id=$1`, observer).Scan(&status))
	require.Equal(t, core.TriggerRunFailed, status)
	incidents, err := f.h.Repo().ListAttentionIncidents(ctx, f.userID)
	require.NoError(t, err)
	require.Len(t, incidents, 1)
	require.Equal(t, "recovering", incidents[0].State)
}
