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

type listRunSeed struct {
	id       string
	user     string
	title    string
	project  string
	workflow string
	status   WorkflowStatus
	at       time.Time
	daemon   string
	archived bool
	noRoot   bool
	launch   core.TriggerEventKind
	trigger  string
	payload  map[string]any
}

// seedListRun creates a chat whose root workflow shares its id, as every run is.
func seedListRun(t *testing.T, repo *Repo, s listRunSeed) {
	t.Helper()
	ctx := context.Background()
	if s.user == "" {
		s.user = "test-user"
	}
	if s.project == "" {
		s.project = "test-project"
	}
	if s.workflow == "" {
		s.workflow = "builtin://agent"
	}
	if s.at.IsZero() {
		s.at = time.Now().UTC().Truncate(time.Microsecond)
	}
	state := ChatStateIdle
	if s.archived {
		state = ChatStateArchived
	}
	workflowID := s.id
	chat := &Chat{
		ID: s.id, Title: s.title, ProjectID: s.project, UserID: s.user,
		WorkflowName: &s.workflow, State: state,
		CreatedAt: s.at, UpdatedAt: s.at, LastActive: s.at,
	}
	if !s.noRoot {
		chat.WorkflowID = &workflowID
	}
	if s.daemon != "" {
		chat.ActiveDaemonID = &s.daemon
	}
	require.NoError(t, repo.CreateChat(ctx, chat))
	createTestRootThread(t, repo, s.id)
	if !s.noRoot {
		wf := &Workflow{
			ID: s.id, ChatID: s.id, WorkflowName: s.workflow, Thread: s.id,
			Status: s.status, CreatedAt: s.at,
		}
		if s.status.State == core.WorkflowStateStopped {
			completed := s.at.Add(time.Minute)
			wf.CompletedAt = &completed
		}
		require.NoError(t, repo.CreateWorkflow(ctx, wf))
	}
	if s.launch != "" {
		var triggerRef *string
		if s.trigger != "" {
			triggerRef = &s.trigger
		}
		payload := s.payload
		if payload == nil {
			payload = map[string]any{}
		}
		created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID: "evt-" + s.id, TriggerID: triggerRef, UserID: s.user, Kind: s.launch,
			DedupeKey: "dedupe-" + s.id, OccurredAt: s.at, Payload: payload,
			Outcome: core.TriggerEventLaunched, ChatID: &s.id, CreatedAt: s.at,
		})
		require.NoError(t, err)
		require.True(t, created)
	}
}

func listRunIDs(items []*RunListItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ChatID
	}
	return ids
}

func TestListRuns_ScopedToUser(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	seedListRun(t, repo, listRunSeed{id: "mine", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "theirs", user: "someone-else", status: Active()})

	items, _, err := repo.ListRuns(context.Background(), RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"mine"}, listRunIDs(items))

	_, _, err = repo.ListRuns(context.Background(), RunListFilters{Limit: 50})
	assert.Error(t, err, "an empty user must be refused, not read as 'all users'")
}

func TestListRuns_RootRunsOnlyAndArchivedHidden(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	seedListRun(t, repo, listRunSeed{id: "root", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "arch", status: Completed(), archived: true})
	// A child workflow in the root's chat must not surface as its own run.
	parent := "root"
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID: "child", ParentID: &parent, ChatID: "root", WorkflowName: "builtin://agent",
		Thread: "child", Status: Active(), CreatedAt: time.Now(),
	}))

	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"root"}, listRunIDs(items))

	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50, IncludeArchived: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root", "arch"}, listRunIDs(items))
}

func TestListRuns_Filters(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Hour)
	trig := "trg-nightly"
	createTestTriggerProject(t, repo, "p1", "test-user")
	createTestTriggerProject(t, repo, "p2", "test-user")
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(trig, "test-user", "p1", "Nightly triage")))

	seedListRun(t, repo, listRunSeed{id: "a", project: "p1", workflow: "wf-one", title: "Fix Login Bug", status: Active(), at: base.Add(1 * time.Hour), launch: core.TriggerEventKindChatStart})
	seedListRun(t, repo, listRunSeed{id: "b", project: "p2", workflow: "wf-two", title: "Nightly sweep", status: Failed(), at: base.Add(2 * time.Hour), launch: core.TriggerEventKindSchedule, trigger: trig})
	seedListRun(t, repo, listRunSeed{id: "c", project: "p1", workflow: "wf-two", title: "agent child job", status: Completed(), at: base.Add(3 * time.Hour), launch: core.TriggerEventKindAgentStartRun})
	seedListRun(t, repo, listRunSeed{id: "d", project: "p1", workflow: "wf-one", title: "legacy", status: Completed(), at: base.Add(4 * time.Hour)}) // no launch event

	str := func(s string) *string { return &s }
	at := func(h int) *time.Time { v := base.Add(time.Duration(h) * time.Hour); return &v }

	cases := []struct {
		name    string
		filters RunListFilters
		want    []string
	}{
		{"no filters, newest first", RunListFilters{}, []string{"d", "c", "b", "a"}},
		{"project", RunListFilters{ProjectID: str("p1")}, []string{"d", "c", "a"}},
		{"workflow (any of)", RunListFilters{Workflows: []string{"wf-two"}}, []string{"c", "b"}},
		{"two workflows", RunListFilters{Workflows: []string{"wf-one", "wf-two"}}, []string{"d", "c", "b", "a"}},
		{"trigger", RunListFilters{TriggerID: &trig}, []string{"b"}},
		{"launch kind schedule", RunListFilters{LaunchKinds: []string{"schedule"}}, []string{"b"}},
		{"launch kind chat.start also matches a run with no launch event", RunListFilters{LaunchKinds: []string{"chat.start"}}, []string{"d", "a"}},
		{"launch kinds (any of)", RunListFilters{LaunchKinds: []string{"schedule", "agent.start_run"}}, []string{"c", "b"}},
		{"display state failed", RunListFilters{DisplayStates: []RunDisplayState{RunDisplayFailed}}, []string{"b"}},
		{"display states (any of)", RunListFilters{DisplayStates: []RunDisplayState{RunDisplayRunning, RunDisplayCompleted}}, []string{"d", "c", "a"}},
		{"started after is inclusive", RunListFilters{StartedAfter: at(3)}, []string{"d", "c"}},
		{"started before is exclusive", RunListFilters{StartedBefore: at(3)}, []string{"b", "a"}},
		{"time window", RunListFilters{StartedAfter: at(2), StartedBefore: at(4)}, []string{"c", "b"}},
		{"query is case-insensitive substring", RunListFilters{Query: str("login")}, []string{"a"}},
		{"query matches nothing", RunListFilters{Query: str("zzz")}, []string{}},
		{"query with LIKE metacharacters is literal", RunListFilters{Query: str("%")}, []string{}},
		{"filters combine", RunListFilters{ProjectID: str("p1"), Workflows: []string{"wf-one"}, DisplayStates: []RunDisplayState{RunDisplayCompleted}}, []string{"d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.filters.UserID = "test-user"
			tc.filters.Limit = 50
			items, hasMore, err := repo.ListRuns(ctx, tc.filters)
			require.NoError(t, err)
			assert.Equal(t, tc.want, listRunIDs(items))
			assert.False(t, hasMore)
		})
	}
}

func TestListRuns_KeysetPaginationWithTiedCreatedAt(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	// Seven runs, but only three distinct created_at: every page boundary
	// lands inside a tie, which is exactly where an (created_at)-only cursor
	// duplicates or skips rows.
	var want []string
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("run-%02d", i)
		seedListRun(t, repo, listRunSeed{id: id, status: Completed(), at: base.Add(time.Duration(i/3) * time.Minute)})
		want = append(want, id)
	}

	var got []string
	var cursor *RunCursor
	for page := 0; page < 10; page++ {
		items, hasMore, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 2, After: cursor})
		require.NoError(t, err)
		got = append(got, listRunIDs(items)...)
		if !hasMore {
			break
		}
		last := items[len(items)-1]
		cursor = &RunCursor{CreatedAt: last.CreatedAt, ChatID: last.ChatID}
	}

	require.Len(t, got, len(want), "no gaps and no duplicates: %v", got)
	assert.ElementsMatch(t, want, got)
	unique := map[string]bool{}
	for _, id := range got {
		assert.False(t, unique[id], "duplicate %s in %v", id, got)
		unique[id] = true
	}
	assert.Equal(t, []string{"run-06", "run-05", "run-04", "run-03", "run-02", "run-01", "run-00"}, got,
		"newest first; created_at ties break by id descending")
}

func TestListRuns_HasMoreOnlyWhenMoreRowsFollow(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		seedListRun(t, repo, listRunSeed{id: fmt.Sprintf("r%d", i), status: Completed(), at: time.Now().UTC().Add(time.Duration(i) * time.Second)})
	}
	items, hasMore, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 3})
	require.NoError(t, err)
	assert.Len(t, items, 3)
	assert.False(t, hasMore, "a full final page is not 'more'")
	items, hasMore, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 2})
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.True(t, hasMore)
}

func TestListRuns_DisplayStates(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	seedListRun(t, repo, listRunSeed{id: "queued", status: Pending()})
	seedListRun(t, repo, listRunSeed{id: "noroot", noRoot: true})
	seedListRun(t, repo, listRunSeed{id: "running", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "needs", status: Active()})
	seedListRun(t, repo, listRunSeed{id: "paused", status: Paused()})
	seedListRun(t, repo, listRunSeed{id: "done", status: Completed()})
	seedListRun(t, repo, listRunSeed{id: "failed", status: Failed()})
	seedListRun(t, repo, listRunSeed{id: "cancelled", status: Cancelled()})
	insertTestApproval(t, repo, "appr-needs", "needs", 1) // pending approval => activity AWAITING_INPUT

	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	got := map[string]RunDisplayState{}
	for _, item := range items {
		got[item.ChatID] = item.DisplayState
	}
	assert.Equal(t, map[string]RunDisplayState{
		"queued": RunDisplayQueued, "noroot": RunDisplayQueued,
		"running": RunDisplayRunning, "needs": RunDisplayNeedsInput,
		"paused": RunDisplayPaused, "done": RunDisplayCompleted,
		"failed": RunDisplayFailed, "cancelled": RunDisplayCancelled,
	}, got)

	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50, DisplayStates: []RunDisplayState{RunDisplayNeedsInput}})
	require.NoError(t, err)
	assert.Equal(t, []string{"needs"}, listRunIDs(items), "needs-input is ACTIVE + AWAITING_INPUT only")

	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50, DisplayStates: []RunDisplayState{RunDisplayRunning}})
	require.NoError(t, err)
	assert.Equal(t, []string{"running"}, listRunIDs(items), "a run awaiting input is not 'running'")
}

func TestListRuns_CarriesChatAndTriggerColumns(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	trig := "trg-names"
	createTestTriggerProject(t, repo, "p1", "test-user")
	require.NoError(t, repo.CreateTrigger(ctx, newTestTrigger(trig, "test-user", "p1", "Nightly triage")))
	seedListRun(t, repo, listRunSeed{id: "sched", project: "p1", title: "Nightly sweep", status: Completed(), daemon: "daemon-9", launch: core.TriggerEventKindSchedule, trigger: trig})
	seedListRun(t, repo, listRunSeed{id: "adhoc", status: Active()})

	items, _, err := repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	byID := map[string]*RunListItem{}
	for _, item := range items {
		byID[item.ChatID] = item
	}
	sched := byID["sched"]
	require.NotNil(t, sched)
	assert.Equal(t, "Nightly sweep", sched.Title)
	assert.Equal(t, "p1", sched.ProjectID)
	assert.Equal(t, "schedule", sched.LaunchKind)
	assert.Equal(t, trig, sched.TriggerID)
	assert.Equal(t, "Nightly triage", sched.TriggerName, "trigger_name comes from the triggers join")
	assert.Equal(t, "daemon-9", sched.DaemonID)
	assert.Equal(t, "sched", sched.RunID)
	assert.Equal(t, "builtin://agent", sched.WorkflowName)
	assert.NotNil(t, sched.CompletedAt, "completed_at comes from the root workflow row")
	adhoc := byID["adhoc"]
	assert.Empty(t, adhoc.TriggerName)
	assert.Empty(t, adhoc.DaemonID)

	// Deleting the trigger leaves the run listed, with a blank name.
	require.NoError(t, repo.DeleteTrigger(ctx, trig))
	items, _, err = repo.ListRuns(ctx, RunListFilters{UserID: "test-user", Limit: 50, Workflows: []string{"builtin://agent"}})
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestLastRunPerWorkflow(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	createTestTriggerProject(t, repo, "p2", "test-user")
	seedListRun(t, repo, listRunSeed{id: "one-old", workflow: "wf-one", status: Completed(), at: base})
	seedListRun(t, repo, listRunSeed{id: "one-new", workflow: "wf-one", status: Failed(), at: base.Add(time.Minute)})
	seedListRun(t, repo, listRunSeed{id: "two-only", workflow: "wf-two", status: Active(), at: base.Add(2 * time.Minute)})
	seedListRun(t, repo, listRunSeed{id: "three-p2", workflow: "wf-three", project: "p2", status: Completed(), at: base})
	seedListRun(t, repo, listRunSeed{id: "other-user", user: "someone-else", workflow: "wf-four", status: Completed(), at: base})
	seedListRun(t, repo, listRunSeed{id: "one-archived", workflow: "wf-one", status: Completed(), at: base.Add(time.Hour / 2), archived: true})

	items, err := repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user"})
	require.NoError(t, err)
	got := map[string]string{}
	for _, item := range items {
		got[item.WorkflowName] = item.ChatID
	}
	assert.Equal(t, map[string]string{"wf-one": "one-new", "wf-two": "two-only", "wf-three": "three-p2"}, got,
		"one newest run per workflow, only the caller's, archived ignored")
	assert.Equal(t, RunDisplayFailed, items[0].DisplayState) // ordered by workflow name: wf-one first
	assert.Equal(t, "wf-one", items[0].WorkflowName)

	p2 := "p2"
	items, err = repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user", ProjectID: &p2})
	require.NoError(t, err)
	assert.Equal(t, []string{"three-p2"}, listRunIDs(items))

	items, err = repo.LastRunPerWorkflow(ctx, RunListFilters{UserID: "test-user", Workflows: []string{"wf-two"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"two-only"}, listRunIDs(items))
}
