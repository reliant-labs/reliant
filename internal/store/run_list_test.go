// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

type runSeed struct {
	id, user, workflow, title string
	status                    core.WorkflowStatus
	noRoot, archived          bool
	launch                    core.TriggerEventKind
	parent                    string
	daemon                    string
	at                        time.Time // zero: l2Base plus the seed's index in minutes
}

// seedRuns writes runs the way the product does: a chat whose root workflow
// shares its id, and a launch event for a non-interactive start.
func seedRuns(t *testing.T, env *storetest.Env, seeds []runSeed) {
	t.Helper()
	ctx := context.Background()
	for i, s := range seeds {
		at := l2Base.Add(time.Duration(i) * time.Minute)
		if !s.at.IsZero() {
			at = s.at
		}
		if s.user == "" {
			s.user = storetest.SeedUserID
		}
		if s.workflow == "" {
			s.workflow = "builtin://agent"
		}
		if s.title == "" {
			s.title = "Run " + s.id
		}
		chat := &core.Chat{
			ID: s.id, Title: s.title, ProjectID: storetest.SeedProjectID, UserID: s.user,
			WorkflowName: ptr(s.workflow), State: core.ChatStateIdle,
			CreatedAt: at, UpdatedAt: at, LastActive: at.Add(time.Duration(len(seeds)-i) * time.Hour),
		}
		if s.archived {
			chat.State = core.ChatStateArchived
		}
		if !s.noRoot {
			chat.WorkflowID = ptr(s.id)
		}
		if s.daemon != "" {
			chat.ActiveDaemonID = ptr(s.daemon)
		}
		if err := env.Repo.CreateChat(ctx, chat); err != nil {
			t.Fatalf("seed chat %s: %v", s.id, err)
		}
		if !s.noRoot {
			w := newWorkflow(s.id, s.id, nil, s.status, at)
			w.WorkflowName = s.workflow
			if s.status.State == core.WorkflowStateStopped && s.status.StopReason != core.StopReasonPaused {
				w.CompletedAt = ptr(at.Add(time.Minute))
			}
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
				t.Fatal(err)
			}
			if w.CompletedAt != nil {
				if err := store.SetWorkflowOutcome(ctx, env.ORM, w.ID, "success"); err != nil {
					t.Fatal(err)
				}
			}
		}
		if s.launch != "" {
			payload := map[string]any{}
			if s.parent != "" {
				payload["parent_chat_id"] = s.parent
			}
			if _, err := env.Repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
				ID: "evt-" + s.id, UserID: s.user, Kind: s.launch, DedupeKey: "dedupe-" + s.id,
				OccurredAt: at, Payload: payload, Outcome: core.TriggerEventLaunched, ChatID: ptr(s.id), CreatedAt: at,
			}); err != nil {
				t.Fatalf("seed launch event %s: %v", s.id, err)
			}
		}
	}
}

func runIDs(items []*core.RunListItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ChatID
	}
	return ids
}

// seededRuns is the fixture the run-list tests share. Seed i is created at
// l2Base+i minutes and was last active (len-i) hours after that, so the two
// orderings are exact opposites.
var seededRuns = []runSeed{
	{id: "parent", status: core.Active(), daemon: "daemon-1"},
	{id: "done", status: core.Completed(), workflow: "deploy", title: "Deploy prod"},
	{id: "failed", status: core.Failed(), workflow: "deploy"},
	{id: "paused", status: core.Paused()},
	{id: "queued", noRoot: true},
	{id: "archived", status: core.Completed(), archived: true, workflow: "nightly"},
	{id: "test-run", status: core.Completed(), workflow: "scratch", launch: core.TriggerEventKind("builder.test")},
	{id: "child", status: core.Active(), launch: core.TriggerEventKind("agent.start_run"), parent: "parent"},
	{id: "theirs", status: core.Active(), user: "someone-else"},
}

// wantRun is the row seededRuns[idx] lists as: the defaults every seed gets,
// then the fields that make this one different.
func wantRun(idx int, differs func(*core.RunListItem)) *core.RunListItem {
	id := seededRuns[idx].id
	at := l2Base.Add(time.Duration(idx) * time.Minute)
	item := &core.RunListItem{
		RunID: id, ChatID: id, Title: "Run " + id, ProjectID: storetest.SeedProjectID,
		WorkflowName: "builtin://agent", CreatedAt: at, LastActive: at.Add(time.Duration(len(seededRuns)-idx) * time.Hour),
	}
	differs(item)
	return item
}

// ended marks a run that finished a minute after it started with outcome "success".
func ended(item *core.RunListItem) {
	item.CompletedAt = ptr(item.CreatedAt.Add(time.Minute))
	item.Outcome = "success"
}

func TestListRunsRows(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedRuns(t, env, seededRuns)
	user := storetest.SeedUserID

	// Every column of every listed run, newest first. Builder test runs and
	// someone else's runs are not in it.
	want := []*core.RunListItem{
		wantRun(7, func(r *core.RunListItem) {
			r.RootStatus, r.Activity, r.DisplayState = core.Active(), 1, core.RunDisplayRunning
			r.LaunchKind, r.ParentChatID, r.ParentChatTitle = "agent.start_run", "parent", "Run parent"
		}),
		wantRun(5, func(r *core.RunListItem) {
			ended(r)
			r.WorkflowName, r.RootStatus, r.DisplayState, r.Archived = "nightly", core.Completed(), core.RunDisplayCompleted, true
		}),
		wantRun(4, func(r *core.RunListItem) { r.DisplayState = core.RunDisplayQueued }), // no root workflow row yet: zero RootStatus
		wantRun(3, func(r *core.RunListItem) {
			r.RootStatus, r.Activity, r.DisplayState = core.Paused(), 4, core.RunDisplayPaused // paused is not ended: no CompletedAt
		}),
		wantRun(2, func(r *core.RunListItem) {
			ended(r)
			r.WorkflowName, r.RootStatus, r.Activity, r.DisplayState = "deploy", core.Failed(), 3, core.RunDisplayFailed
		}),
		wantRun(1, func(r *core.RunListItem) {
			ended(r)
			r.Title, r.WorkflowName, r.RootStatus, r.DisplayState = "Deploy prod", "deploy", core.Completed(), core.RunDisplayCompleted
		}),
		wantRun(0, func(r *core.RunListItem) {
			r.RootStatus, r.Activity, r.DisplayState, r.DaemonID = core.Active(), 1, core.RunDisplayRunning, "daemon-1"
		}),
	}
	got, more, err := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: user, Limit: 50, IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("hasMore with every run in a page of 50")
	}
	requireSame(t, "all runs", got, want)

	// The builder test run appears only when asked for by its launch kind.
	tests, _, err := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"builder.test"}})
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "builder tests", tests, []*core.RunListItem{wantRun(6, func(r *core.RunListItem) {
		ended(r)
		r.WorkflowName, r.RootStatus, r.DisplayState, r.LaunchKind = "scratch", core.Completed(), core.RunDisplayCompleted, "builder.test"
	})})

	// A parent that is not the caller's contributes no title.
	seedRuns(t, env, []runSeed{{id: "orphan-child", status: core.Active(), launch: core.TriggerEventKind("agent.start_run"), parent: "theirs", at: l2Base.Add(time.Hour)}})
	orphan, _, err := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: user, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphan) != 1 || orphan[0].ChatID != "orphan-child" || orphan[0].ParentChatID != "theirs" || orphan[0].ParentChatTitle != "" {
		t.Fatalf("a child of someone else's chat = %+v, want ParentChatID theirs and no title", orphan)
	}
}

func TestListRunsFilters(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedRuns(t, env, seededRuns)

	user := storetest.SeedUserID
	after := l2Base.Add(90 * time.Second)
	cursor := &core.RunCursor{CreatedAt: l2Base.Add(5 * time.Minute), ChatID: "archived"}
	shapes := []struct {
		name    string
		filters core.RunListFilters
		want    string
		more    bool
	}{
		{"default", core.RunListFilters{UserID: user, Limit: 50}, "[child queued paused failed done parent]", false},
		{"archived included", core.RunListFilters{UserID: user, Limit: 50, IncludeArchived: true}, "[child archived queued paused failed done parent]", false},
		{"page of two", core.RunListFilters{UserID: user, Limit: 2}, "[child queued]", true},
		{"page that is exactly the rest", core.RunListFilters{UserID: user, Limit: 6}, "[child queued paused failed done parent]", false},
		{"one short of the rest", core.RunListFilters{UserID: user, Limit: 5}, "[child queued paused failed done]", true},
		{"after a cursor", core.RunListFilters{UserID: user, Limit: 50, After: cursor}, "[queued paused failed done parent]", false},
		{"after a cursor, paged", core.RunListFilters{UserID: user, Limit: 2, After: cursor}, "[queued paused]", true},
		{"cursor at the oldest run", core.RunListFilters{UserID: user, Limit: 50, After: &core.RunCursor{CreatedAt: l2Base, ChatID: "parent"}}, "[]", false},
		{"cursor on a run excludes it", core.RunListFilters{UserID: user, Limit: 50, After: &core.RunCursor{CreatedAt: l2Base.Add(time.Minute), ChatID: "done"}}, "[parent]", false},
		{"cursor at the same instant, larger id", core.RunListFilters{UserID: user, Limit: 50, After: &core.RunCursor{CreatedAt: l2Base.Add(time.Minute), ChatID: "e"}}, "[done parent]", false},
		{"cursor at the same instant, smaller id", core.RunListFilters{UserID: user, Limit: 50, After: &core.RunCursor{CreatedAt: l2Base.Add(time.Minute), ChatID: "a"}}, "[parent]", false},
		{"by last active", core.RunListFilters{UserID: user, Limit: 50, ByLastActive: true}, "[parent done failed paused queued child]", false},
		{"by last active, paged", core.RunListFilters{UserID: user, Limit: 2, ByLastActive: true}, "[parent done]", true},
		{"workflows", core.RunListFilters{UserID: user, Limit: 50, Workflows: []string{"deploy"}}, "[failed done]", false},
		{"several workflows", core.RunListFilters{UserID: user, Limit: 50, Workflows: []string{"deploy", "nightly"}, IncludeArchived: true}, "[archived failed done]", false},
		{"unknown workflow", core.RunListFilters{UserID: user, Limit: 50, Workflows: []string{"nope"}}, "[]", false},
		{"project", core.RunListFilters{UserID: user, Limit: 50, ProjectID: ptr(storetest.SeedProjectID)}, "[child queued paused failed done parent]", false},
		{"other project", core.RunListFilters{UserID: user, Limit: 50, ProjectID: ptr("nope")}, "[]", false},
		{"builder tests", core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"builder.test"}}, "[test-run]", false},
		{"interactive", core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"chat.start"}}, "[queued paused failed done parent]", false},
		{"agent started and tests", core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"agent.start_run", "builder.test"}}, "[child test-run]", false},
		{"display states", core.RunListFilters{UserID: user, Limit: 50, DisplayStates: []core.RunDisplayState{core.RunDisplayFailed, core.RunDisplayQueued}}, "[queued failed]", false},
		{"running", core.RunListFilters{UserID: user, Limit: 50, DisplayStates: []core.RunDisplayState{core.RunDisplayRunning}}, "[child parent]", false},
		{"started window", core.RunListFilters{UserID: user, Limit: 50, StartedAfter: &after, StartedBefore: ptr(l2Base.Add(4 * time.Minute))}, "[paused failed]", false},
		{"started-after is inclusive", core.RunListFilters{UserID: user, Limit: 50, StartedAfter: ptr(l2Base.Add(time.Minute)), StartedBefore: ptr(l2Base.Add(2 * time.Minute))}, "[done]", false},
		{"started-before is exclusive", core.RunListFilters{UserID: user, Limit: 50, StartedBefore: ptr(l2Base.Add(time.Minute))}, "[parent]", false},
		{"query, any case", core.RunListFilters{UserID: user, Limit: 50, Query: ptr("PROD")}, "[done]", false},
		{"query matching every title", core.RunListFilters{UserID: user, Limit: 50, Query: ptr("run ")}, "[child queued paused failed parent]", false},
		{"query matching none", core.RunListFilters{UserID: user, Limit: 50, Query: ptr("zzz")}, "[]", false},
		{"children of", core.RunListFilters{UserID: user, Limit: 50, ParentChatID: ptr("parent")}, "[child]", false},
		{"children of a run with none", core.RunListFilters{UserID: user, Limit: 50, ParentChatID: ptr("done")}, "[]", false},
		{"trigger", core.RunListFilters{UserID: user, Limit: 50, TriggerID: ptr("trg-none")}, "[]", false},
		{"filters combine", core.RunListFilters{UserID: user, Limit: 50, Workflows: []string{"deploy"}, DisplayStates: []core.RunDisplayState{core.RunDisplayFailed}}, "[failed]", false},
		{"another user", core.RunListFilters{UserID: "someone-else", Limit: 50}, "[theirs]", false},
		{"a user with no runs", core.RunListFilters{UserID: "nobody", Limit: 50}, "[]", false},
	}
	for _, s := range shapes {
		got, more, err := store.ListRuns(ctx, env.ORM, s.filters)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if g := fmt.Sprint(runIDs(got)); g != s.want || more != s.more {
			t.Fatalf("%s: listed %s (hasMore %v), want %s (hasMore %v)", s.name, g, more, s.want, s.more)
		}
	}
}

// TestListRunsPagesThroughTies: runs created at the same instant list by chat
// id, newest id first, and a cursor on a tied run continues after it.
func TestListRunsPagesThroughTies(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	tied := l2Base.Add(time.Hour)
	seedRuns(t, env, []runSeed{
		{id: "early", status: core.Completed(), at: l2Base},
		{id: "tie-a", status: core.Completed(), at: tied},
		{id: "tie-c", status: core.Completed(), at: tied},
		{id: "tie-b", status: core.Completed(), at: tied},
	})
	filters := core.RunListFilters{UserID: storetest.SeedUserID, Limit: 1}

	var seen []string
	for page := 0; page < 10; page++ {
		got, more, err := store.ListRuns(ctx, env.ORM, filters)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("page %d has %d runs, want 1", page, len(got))
		}
		seen = append(seen, got[0].ChatID)
		if !more {
			break
		}
		filters.After = &core.RunCursor{CreatedAt: got[0].CreatedAt, ChatID: got[0].ChatID}
	}
	if fmt.Sprint(seen) != "[tie-c tie-b tie-a early]" {
		t.Fatalf("paging one at a time saw %v, want [tie-c tie-b tie-a early]", seen)
	}
	if filters.After.ChatID != "tie-a" {
		t.Fatalf("the last cursor was %+v, want the run before the final page", filters.After)
	}
	rest, more, err := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: storetest.SeedUserID, Limit: 50, After: &core.RunCursor{CreatedAt: l2Base, ChatID: "early"}})
	if err != nil || more || len(rest) != 0 {
		t.Fatalf("a cursor on the oldest run listed %v, hasMore %v, %v; want nothing", runIDs(rest), more, err)
	}
}

func TestListRunsRefusals(t *testing.T) {
	env := storetest.New(t)
	user := storetest.SeedUserID
	for _, bad := range []struct {
		name    string
		filters core.RunListFilters
		want    string
	}{
		{"no user", core.RunListFilters{Limit: 50}, "user ID cannot be empty"},
		{"no limit", core.RunListFilters{UserID: user}, "limit must be positive"},
		{"negative limit", core.RunListFilters{UserID: user, Limit: -1}, "limit must be positive"},
		{"cursor with last-active order", core.RunListFilters{UserID: user, Limit: 5, ByLastActive: true, After: &core.RunCursor{CreatedAt: l2Base, ChatID: "x"}}, "a page cursor cannot be combined with last-active ordering"},
	} {
		_, _, err := store.ListRuns(context.Background(), env.ORM, bad.filters)
		if err == nil || err.Error() != bad.want {
			t.Fatalf("%s: err = %v, want %q", bad.name, err, bad.want)
		}
	}
	if _, err := store.LastRunPerWorkflow(context.Background(), env.ORM, core.RunListFilters{}); err == nil || err.Error() != "user ID cannot be empty" {
		t.Fatalf("LastRunPerWorkflow with no user: %v", err)
	}
}

func TestLastRunPerWorkflow(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedRuns(t, env, seededRuns)
	user := storetest.SeedUserID

	cases := []struct {
		name    string
		filters core.RunListFilters
		want    string
	}{
		// By workflow name: the agent's newest is child, deploy's is failed, and
		// archived runs count. The builder test run's workflow ("scratch") is absent.
		{"every workflow", core.RunListFilters{UserID: user}, "[child failed archived]"},
		{"named", core.RunListFilters{UserID: user, Workflows: []string{"deploy"}}, "[failed]"},
		{"two named", core.RunListFilters{UserID: user, Workflows: []string{"nightly", "deploy"}}, "[failed archived]"},
		{"unknown name", core.RunListFilters{UserID: user, Workflows: []string{"nope"}}, "[]"},
		{"project", core.RunListFilters{UserID: user, ProjectID: ptr(storetest.SeedProjectID)}, "[child failed archived]"},
		{"other project", core.RunListFilters{UserID: user, ProjectID: ptr("nope")}, "[]"},
		// Only the user, project and workflow filters apply.
		{"children of is ignored", core.RunListFilters{UserID: user, ParentChatID: ptr("parent")}, "[child failed archived]"},
		{"limit and states are ignored", core.RunListFilters{UserID: user, Limit: 1, DisplayStates: []core.RunDisplayState{core.RunDisplayRunning}}, "[child failed archived]"},
		{"another user", core.RunListFilters{UserID: "someone-else"}, "[theirs]"},
		{"a user with no runs", core.RunListFilters{UserID: "nobody"}, "[]"},
	}
	for _, c := range cases {
		got, err := store.LastRunPerWorkflow(ctx, env.ORM, c.filters)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g := fmt.Sprint(runIDs(got)); g != c.want {
			t.Fatalf("%s: listed %s, want %s", c.name, g, c.want)
		}
	}

	// A tie on the newest created_at goes to the larger chat id.
	seedRuns(t, env, []runSeed{
		{id: "dup-a", status: core.Completed(), workflow: "twin", at: l2Base.Add(time.Hour)},
		{id: "dup-b", status: core.Failed(), workflow: "twin", at: l2Base.Add(time.Hour)},
	})
	got, err := store.LastRunPerWorkflow(ctx, env.ORM, core.RunListFilters{UserID: user, Workflows: []string{"twin"}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(runIDs(got)) != "[dup-b]" || got[0].DisplayState != core.RunDisplayFailed {
		t.Fatalf("tie on created_at: %v, want dup-b", runIDs(got))
	}
}
