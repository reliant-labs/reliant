// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// An automation that fails every hour must notify once per failure streak, not
// once per failure (WORKFLOW_UI.md §6.4). A success ends the streak.

type streakFixture struct {
	t         *testing.T
	h         *IdempotencyTestHelper
	userID    string
	projectID string
	triggerID string
	clock     time.Time
}

func newStreakFixture(t *testing.T, notifyOnComplete bool) *streakFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	f := &streakFixture{
		t: t, h: h, userID: uuid.NewString(), projectID: uuid.NewString(),
		triggerID: uuid.NewString(), clock: time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond),
	}
	h.CreateTestProject(ctx, f.projectID, f.userID)
	cfg, _ := json.Marshal(core.ScheduleConfig{Interval: "1h"})
	require.NoError(t, h.Repo().CreateTrigger(ctx, &core.Trigger{
		ID: f.triggerID, UserID: f.userID, ProjectID: f.projectID, Name: "hourly",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "wf", DaemonID: "d1",
		Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
		NotifyOnComplete: notifyOnComplete,
		CreatedAt:        f.clock, UpdatedAt: f.clock,
	}))
	return f
}

// launch creates a scheduled run (chat + root workflow + launched event) in the
// given state and returns its chat id. Each call is one hour after the last.
func (f *streakFixture) launch(kind core.TriggerEventKind, status db.WorkflowStatus) string {
	f.t.Helper()
	ctx := context.Background()
	f.clock = f.clock.Add(time.Hour)
	chatID := uuid.NewString()
	// Not h.CreateTestChat: it also makes a shared "0" thread, so a second chat
	// in the same database would collide.
	require.NoError(f.t, f.h.Repo().CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: f.projectID, UserID: f.userID}))
	_, err := f.h.Repo().CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
	require.NoError(f.t, err)
	require.NoError(f.t, f.h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID, Status: status,
	}))
	// chats_with_activity derives the run's display state through
	// chats.workflow_id; without it every run reads as queued.
	_, err = f.h.DB().ExecContext(ctx, `UPDATE chats SET workflow_id = $1 WHERE id = $2`, chatID, chatID)
	require.NoError(f.t, err)
	tid := f.triggerID
	created, err := f.h.Repo().CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &tid, UserID: f.userID, Kind: kind,
		DedupeKey: "dd-" + chatID, OccurredAt: f.clock, Payload: map[string]any{},
		Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: f.clock,
	})
	require.NoError(f.t, err)
	require.True(f.t, created)
	return chatID
}

// finish reports the root's terminal status through the activity and returns
// the chat's unread flag and the notify reason.
func (f *streakFixture) finish(chatID, status string) (bool, string) {
	f.t.Helper()
	ctx := context.Background()
	in := WorkflowStatusInput{ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: status, Thread: chatID}
	var out WorkflowStatusOutput
	require.NoError(f.t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, in, &out))
	chat, err := f.h.Repo().GetChat(ctx, chatID)
	require.NoError(f.t, err)
	reason := ""
	updates, err := f.h.Repo().GetUserUpdatesSince(ctx, f.userID, 0, 200)
	require.NoError(f.t, err)
	for _, u := range updates {
		var data struct {
			Reason string `json:"reason"`
			Unread *bool  `json:"unread"`
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(u.Data, &data) == nil && data.Unread != nil && *data.Unread && data.ChatID == chatID {
			reason = data.Reason
		}
	}
	return chat.Unread, reason
}

func TestWorkflowStatus_ScheduleFailureNotifiesOncePerStreak(t *testing.T) {
	f := newStreakFixture(t, false)

	first := f.launch(core.TriggerEventKindSchedule, db.Active())
	unread, reason := f.finish(first, "failed")
	assert.True(t, unread, "the first failure of a streak notifies")
	assert.Equal(t, "workflow_failed", reason)

	second := f.launch(core.TriggerEventKindSchedule, db.Active())
	unread, _ = f.finish(second, "failed")
	assert.False(t, unread, "a consecutive failure of the same automation does not notify again")

	third := f.launch(core.TriggerEventKindSchedule, db.Active())
	unread, _ = f.finish(third, "failed")
	assert.False(t, unread, "nor does the third")

	ok := f.launch(core.TriggerEventKindSchedule, db.Active())
	f.finish(ok, "completed")

	afterSuccess := f.launch(core.TriggerEventKindSchedule, db.Active())
	unread, reason = f.finish(afterSuccess, "failed")
	assert.True(t, unread, "a success ends the streak, so the next failure notifies again")
	assert.Equal(t, "workflow_failed", reason)
}

func TestWorkflowStatus_StreakIsPerAutomation(t *testing.T) {
	f := newStreakFixture(t, false)
	other := newStreakFixtureOn(t, f, "other")

	f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "failed")
	unread, _ := f.finish(other.launch(core.TriggerEventKindSchedule, db.Active()), "failed")
	assert.True(t, unread, "another automation's failure is its own first failure")
}

// newStreakFixtureOn adds a second trigger for the same user and project.
func newStreakFixtureOn(t *testing.T, base *streakFixture, name string) *streakFixture {
	t.Helper()
	other := *base
	other.triggerID = uuid.NewString()
	cfg, _ := json.Marshal(core.ScheduleConfig{Interval: "1h"})
	require.NoError(t, base.h.Repo().CreateTrigger(context.Background(), &core.Trigger{
		ID: other.triggerID, UserID: base.userID, ProjectID: base.projectID, Name: name,
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "wf", DaemonID: "d1",
		Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
		CreatedAt: base.clock, UpdatedAt: base.clock,
	}))
	return &other
}

func TestWorkflowStatus_AgentStartedFailureAlwaysNotifies(t *testing.T) {
	f := newStreakFixture(t, false)
	for i := 0; i < 3; i++ {
		chatID := f.launch(core.TriggerEventKindAgentStartRun, db.Active())
		unread, reason := f.finish(chatID, "failed")
		assert.True(t, unread, "agent-started failure %d notifies", i)
		assert.Equal(t, "workflow_failed", reason)
	}
}

func TestWorkflowStatus_InteractiveFailureAlwaysNotifiesAfterScheduleFailures(t *testing.T) {
	f := newStreakFixture(t, false)
	f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "failed")
	for i := 0; i < 2; i++ {
		chatID := f.launch(core.TriggerEventKindChatStart, db.Active())
		unread, _ := f.finish(chatID, "failed")
		assert.True(t, unread, "interactive failure %d notifies", i)
	}
}

func TestWorkflowStatus_NotifyOnCompleteOptIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		optIn      bool
		wantUnread bool
	}{
		{"opted in notifies on completion", true, true},
		{"not opted in stays silent", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreakFixture(t, tc.optIn)
			unread, reason := f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "completed")
			assert.Equal(t, tc.wantUnread, unread)
			if tc.wantUnread {
				assert.Equal(t, "workflow_completed", reason)
			}
		})
	}
}
