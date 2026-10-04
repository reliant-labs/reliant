// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func feedTrigger(t *testing.T, repo *Repo, id string) *core.Trigger {
	t.Helper()
	tr := newTestTrigger(id, "test-user", "test-project", id)
	require.NoError(t, repo.CreateTrigger(context.Background(), tr))
	return tr
}

func feedEvent(t *testing.T, repo *Repo, id, triggerID, userID string, outcome core.TriggerEventOutcome, at time.Time) {
	t.Helper()
	ev := newTestTriggerEvent(id, triggerID, userID, "dedupe-"+id, at)
	ev.Outcome = outcome
	created, err := repo.CreateTriggerEvent(context.Background(), ev)
	require.NoError(t, err)
	require.True(t, created)
}

func TestListTriggerEvents_PaginatesTiedOccurredAt(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := feedTrigger(t, repo, "trg-tie")

	// Five firings share one instant; a cursor on occurred_at alone would drop
	// or repeat rows at a page boundary. The one older row proves the cursor
	// still advances past the tie.
	tied := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		feedEvent(t, repo, fmt.Sprintf("ev-tie-%d", i), tr.ID, "test-user", core.TriggerEventSkipped, tied)
	}
	feedEvent(t, repo, "ev-tie-old", tr.ID, "test-user", core.TriggerEventSkipped, tied.Add(-time.Hour))

	var seen []string
	var cursor *core.TriggerEventCursor
	for page := 0; page < 10; page++ {
		items, hasMore, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{
			UserID: "test-user", TriggerID: tr.ID, Limit: 2, After: cursor,
		})
		require.NoError(t, err)
		for _, it := range items {
			seen = append(seen, it.Event.ID)
		}
		if !hasMore {
			break
		}
		last := items[len(items)-1].Event
		cursor = &core.TriggerEventCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	assert.Equal(t, []string{"ev-tie-4", "ev-tie-3", "ev-tie-2", "ev-tie-1", "ev-tie-0", "ev-tie-old"}, seen,
		"every firing exactly once, newest first, ties broken by id descending")
}

func TestListTriggerEvents_OutcomeFilter(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := feedTrigger(t, repo, "trg-outcome")
	base := time.Now().UTC().Truncate(time.Microsecond)
	feedEvent(t, repo, "ev-l", tr.ID, "test-user", core.TriggerEventLaunched, base.Add(-3*time.Minute))
	feedEvent(t, repo, "ev-s", tr.ID, "test-user", core.TriggerEventSkipped, base.Add(-2*time.Minute))
	feedEvent(t, repo, "ev-f", tr.ID, "test-user", core.TriggerEventFailed, base.Add(-time.Minute))

	list := func(outcomes ...core.TriggerEventOutcome) []string {
		items, _, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{
			UserID: "test-user", TriggerID: tr.ID, Limit: 10, Outcomes: outcomes,
		})
		require.NoError(t, err)
		return idsOfEvents(items)
	}
	assert.Equal(t, []string{"ev-f", "ev-s", "ev-l"}, list(), "no filter returns every outcome")
	assert.Equal(t, []string{"ev-s"}, list(core.TriggerEventSkipped))
	assert.Equal(t, []string{"ev-f", "ev-l"}, list(core.TriggerEventFailed, core.TriggerEventLaunched))
}

func TestListTriggerEvents_JoinsRunState(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := feedTrigger(t, repo, "trg-runs")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	statuses := map[string]WorkflowStatus{
		"r-queued": Pending(), "r-running": Active(), "r-paused": Paused(),
		"r-done": Completed(), "r-failed": Failed(), "r-cancelled": Cancelled(),
	}
	i := 0
	for id, st := range statuses {
		seedListRun(t, repo, listRunSeed{
			id: id, title: "title of " + id, status: st, launch: core.TriggerEventKindSchedule,
			trigger: tr.ID, at: base.Add(time.Duration(i) * time.Minute),
		})
		i++
	}
	seedListRun(t, repo, listRunSeed{
		id: "r-needs", title: "title of r-needs", status: Active(), launch: core.TriggerEventKindSchedule,
		trigger: tr.ID, at: base.Add(time.Duration(i) * time.Minute),
	})
	insertTestApproval(t, repo, "appr-feed", "r-needs", 1)
	seedListRun(t, repo, listRunSeed{
		id: "r-wait", title: "title of r-wait", status: Active(), launch: core.TriggerEventKindSchedule,
		trigger: tr.ID, at: base.Add(time.Duration(i+1) * time.Minute),
	})
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, "r-wait", true))
	// A skipped firing has no run, and a launched firing whose chat is gone has none either.
	feedEvent(t, repo, "ev-skipped", tr.ID, "test-user", core.TriggerEventSkipped, base.Add(time.Hour))

	items, _, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: "test-user", TriggerID: tr.ID, Limit: 50})
	require.NoError(t, err)
	require.Len(t, items, 9)

	runs, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	wantState := map[string]RunDisplayState{}
	for _, r := range runs {
		wantState[r.ChatID] = r.DisplayState
	}

	got := map[string]*core.TriggerEventRun{}
	for _, it := range items {
		if it.Event.ChatID == nil {
			assert.Nil(t, it.Run, "a firing that launched nothing has no run")
			continue
		}
		require.NotNil(t, it.Run, *it.Event.ChatID)
		got[*it.Event.ChatID] = it.Run
	}
	assert.Equal(t, RunDisplayFailed, got["r-failed"].DisplayState)
	assert.Equal(t, RunDisplayNeedsInput, got["r-needs"].DisplayState)
	assert.Equal(t, "title of r-done", got["r-done"].Title)
	assert.Equal(t, Failed(), got["r-failed"].RootStatus)
	// The same derivation as the Runs list: the two queries can never disagree.
	for chatID, run := range got {
		assert.Equal(t, wantState[chatID], run.DisplayState, "display state of %s must match ListRuns", chatID)
	}
}

func TestListTriggerEvents_ScopedToUser(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := feedTrigger(t, repo, "trg-scope")
	base := time.Now().UTC().Truncate(time.Microsecond)
	feedEvent(t, repo, "ev-mine", tr.ID, "test-user", core.TriggerEventSkipped, base)

	items, _, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: "someone-else", TriggerID: tr.ID, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, items, "another user must never read this trigger's firings")

	recent, err := repo.RecentTriggerFirings(ctx, "someone-else", []string{tr.ID}, 10)
	require.NoError(t, err)
	assert.Empty(t, recent)

	_, _, err = repo.ListTriggerEvents(ctx, core.TriggerEventFilters{TriggerID: tr.ID, Limit: 10})
	assert.Error(t, err, "an empty user must be refused, not read as 'all users'")
}

func TestRecentTriggerFirings_WindowPerTrigger(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := feedTrigger(t, repo, "trg-win-a")
	b := feedTrigger(t, repo, "trg-win-b")
	feedTrigger(t, repo, "trg-win-none")
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		feedEvent(t, repo, fmt.Sprintf("ev-a-%d", i), a.ID, "test-user", core.TriggerEventSkipped, base.Add(time.Duration(i)*time.Minute))
	}
	feedEvent(t, repo, "ev-b-0", b.ID, "test-user", core.TriggerEventFailed, base)

	recent, err := repo.RecentTriggerFirings(ctx, "test-user", []string{a.ID, b.ID, "trg-win-none"}, 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"ev-a-4", "ev-a-3", "ev-a-2"}, idsOfEvents(recent[a.ID]))
	assert.Equal(t, []string{"ev-b-0"}, idsOfEvents(recent[b.ID]))
	assert.NotContains(t, recent, "trg-win-none")
}

func TestTriggerNamesJoined(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	host := "build-box"
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: "daemon-named", UserID: "test-user", Hostname: &host}))
	named := newTestTrigger("trg-named", "test-user", "test-project", "named")
	named.DaemonID = "daemon-named"
	require.NoError(t, repo.CreateTrigger(ctx, named))
	// No daemon row at all: the trigger must still be listed, with an empty name.
	orphan := newTestTrigger("trg-orphan", "test-user", "test-project", "orphan")
	require.NoError(t, repo.CreateTrigger(ctx, orphan))
	// Someone else's daemon row under the same id must not lend its name.
	otherHost := "not-yours"
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: orphan.DaemonID, UserID: "someone-else", Hostname: &otherHost}))

	project, err := repo.GetProjectWithUserCheck(ctx, "test-project", "test-user")
	require.NoError(t, err)

	got, err := repo.GetTrigger(ctx, named.ID)
	require.NoError(t, err)
	assert.Equal(t, project.Name, got.ProjectName)
	assert.Equal(t, "build-box", got.DaemonName)

	listed, err := repo.ListTriggers(ctx, core.TriggerFilters{UserID: "test-user"})
	require.NoError(t, err)
	byID := map[string]*core.Trigger{}
	for _, tr := range listed {
		byID[tr.ID] = tr
	}
	require.Contains(t, byID, "trg-named")
	require.Contains(t, byID, "trg-orphan")
	assert.Equal(t, project.Name, byID["trg-named"].ProjectName)
	assert.Equal(t, "build-box", byID["trg-named"].DaemonName)
	assert.Equal(t, project.Name, byID["trg-orphan"].ProjectName)
	assert.Empty(t, byID["trg-orphan"].DaemonName)
}

func TestFiringsSinceLastSuccess_StopsAtTheNewestSuccessAndIsNotWindowed(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := feedTrigger(t, repo, "trg-since")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)

	// An old failure, a success, then 15 failures: longer than the 10-firing
	// health window, so a window-based count would be wrong.
	feedEvent(t, repo, "ev-old-fail", tr.ID, "test-user", core.TriggerEventFailed, base)
	chatID := "chat-since-ok"
	require.NoError(t, repo.CreateChat(ctx, &Chat{ID: chatID, ProjectID: "test-project", UserID: "test-user", WorkflowID: &chatID}))
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{ID: chatID, ChatID: chatID, WorkflowName: "wf", Thread: chatID, Status: Completed()}))
	ok := newTestTriggerEvent("ev-ok", tr.ID, "test-user", "dd-ev-ok", base.Add(time.Minute))
	ok.Outcome = core.TriggerEventLaunched
	ok.ChatID = &chatID
	created, err := repo.CreateTriggerEvent(ctx, ok)
	require.NoError(t, err)
	require.True(t, created)
	for i := 0; i < 15; i++ {
		feedEvent(t, repo, fmt.Sprintf("ev-fail-%02d", i), tr.ID, "test-user", core.TriggerEventFailed, base.Add(time.Duration(i+2)*time.Minute))
	}

	got, err := repo.FiringsSinceLastSuccess(ctx, "test-user", []string{tr.ID}, 500)
	require.NoError(t, err)
	require.Len(t, got[tr.ID], 15, "only the failures after the success, beyond the 10-firing window")
	assert.Equal(t, "ev-fail-14", got[tr.ID][0].Event.ID, "newest first")
	assert.Equal(t, "ev-fail-00", got[tr.ID][14].Event.ID, "the oldest is the episode's first failure")

	capped, err := repo.FiringsSinceLastSuccess(ctx, "test-user", []string{tr.ID}, 3)
	require.NoError(t, err)
	assert.Len(t, capped[tr.ID], 3)

	other, err := repo.FiringsSinceLastSuccess(ctx, "someone-else", []string{tr.ID}, 500)
	require.NoError(t, err)
	assert.Empty(t, other[tr.ID], "scoped to the user")
}

func TestTrigger_NotifyOnCompleteRoundTrips(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	tr := newTestTrigger("trg-notify", "test-user", "test-project", "notify")
	tr.NotifyOnComplete = true
	require.NoError(t, repo.CreateTrigger(ctx, tr))

	got, err := repo.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.True(t, got.NotifyOnComplete)

	got.NotifyOnComplete = false
	require.NoError(t, repo.UpdateTrigger(ctx, got))
	got, err = repo.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.False(t, got.NotifyOnComplete)
}
