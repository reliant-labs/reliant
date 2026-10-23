// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// An automation archives each run it finishes successfully: the run never was
// in the chat list, and archived it is history in Runs rather than clutter.
// Anything that needs a person — a failure, a run the person adopted, an
// automation that asked to be told — stays unarchived.

// archiveState reports the chat's state after its launch run (or, with
// launchRun false, a later turn) reached status with the declared outcome.
func (f *streakFixture) archiveState(chatID, status, outcome string, launchRun bool) core.ChatState {
	f.t.Helper()
	ctx := context.Background()
	in := WorkflowStatusInput{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: status, Thread: chatID,
		Outcome: outcome, LaunchRun: launchRun,
	}
	var out WorkflowStatusOutput
	require.NoError(f.t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, in, &out))
	chat, err := f.h.Repo().GetChat(ctx, chatID)
	require.NoError(f.t, err)
	return chat.State
}

func TestWorkflowStatus_ArchivesSucceededAutomationRuns(t *testing.T) {
	t.Parallel()

	for _, kind := range storedTriggerKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newStreakFixtureOfKind(t, kind, false)
			chatID := f.launch(kind.EventKind(), db.Active())
			assert.Equal(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true))

			// The archive is an ordinary state change, with a reason a client
			// can tell from a person archiving the chat.
			updates, err := f.h.Repo().GetUserUpdatesSince(context.Background(), f.userID, 0, 200)
			require.NoError(t, err)
			var reasons []string
			for _, u := range updates {
				var data struct {
					Reason string `json:"reason"`
					State  *int32 `json:"state"`
				}
				if json.Unmarshal(u.Data, &data) == nil && data.State != nil {
					reasons = append(reasons, data.Reason)
				}
			}
			assert.Contains(t, reasons, ReasonAutomationSucceeded)
		})
	}
}

func TestWorkflowStatus_KeepsAutomationRunsThatNeedAPerson(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  string
		outcome string
	}{
		{"failed", "failed", ""},
		{"completed into a declared failure outcome", "completed", model.OutcomeFailure},
		{"cancelled", "cancelled", ""},
		{"paused", "paused", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreakFixture(t, false)
			chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
			assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, tc.status, tc.outcome, true))
		})
	}

	t.Run("a declared success outcome is archived", func(t *testing.T) {
		f := newStreakFixture(t, false)
		chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
		assert.Equal(t, db.ChatStateArchived, f.archiveState(chatID, "completed", model.OutcomeSuccess, true))
	})
}

func TestWorkflowStatus_ArchiveLeavesPeoplesChatsAlone(t *testing.T) {
	t.Parallel()

	t.Run("chats and builder tests a person started", func(t *testing.T) {
		for _, kind := range []core.TriggerEventKind{core.TriggerEventKindChatStart, core.TriggerEventKindBuilderTest, core.TriggerEventKindAgentStartRun} {
			f := newStreakFixture(t, false)
			chatID := f.launch(kind, db.Active())
			assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true), "%s", kind)
		}
	})

	t.Run("a chat from before launch events", func(t *testing.T) {
		f := newStreakFixture(t, false)
		ctx := context.Background()
		chatID := uuid.NewString()
		require.NoError(t, f.h.Repo().CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: f.projectID, UserID: f.userID}))
		_, err := f.h.Repo().CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
		require.NoError(t, err)
		require.NoError(t, f.h.Repo().CreateWorkflow(ctx, &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID, Status: db.Active()}))
		assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true))
	})

	t.Run("a turn a person started by replying", func(t *testing.T) {
		f := newStreakFixture(t, false)
		chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
		assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", false))
	})

	t.Run("an adopted run", func(t *testing.T) {
		f := newStreakFixture(t, false)
		chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
		owned, err := f.h.Repo().SetChatAdopted(context.Background(), chatID, f.userID, true)
		require.NoError(t, err)
		require.True(t, owned)
		assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true))
	})
}

// An automation that asked to be told its run finished keeps the run where the
// notification points: that flag is the activation's way to keep its runs.
func TestWorkflowStatus_NotifyOnCompleteKeepsTheRun(t *testing.T) {
	t.Parallel()

	f := newStreakFixture(t, true)
	chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
	assert.NotEqual(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true))
}

// A run whose automation was deleted is still an automation's run.
func TestWorkflowStatus_ArchivesRunOfDeletedAutomation(t *testing.T) {
	t.Parallel()

	f := newStreakFixture(t, false)
	chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
	// The launch event outlives the trigger: trigger_id is SET NULL on delete.
	_, err := f.h.DB().ExecContext(context.Background(), `DELETE FROM triggers WHERE id = $1`, f.triggerID)
	require.NoError(t, err)
	assert.Equal(t, db.ChatStateArchived, f.archiveState(chatID, "completed", "", true))
}
