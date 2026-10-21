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
}

// seedRuns writes runs the way the product does: a chat whose root workflow
// shares its id, and a launch event for a non-interactive start.
func seedRuns(t *testing.T, env *storetest.Env, seeds []runSeed) {
	t.Helper()
	ctx := context.Background()
	for i, s := range seeds {
		at := l2Base.Add(time.Duration(i) * time.Minute)
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
				w.Outcome = ptr("success")
			}
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
				t.Fatal(err)
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

// TestRunListAgreesWithSQLC: every filter shape of the run list returns, row
// for row, what its sqlc twin does.
func TestRunListAgreesWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedRuns(t, env, []runSeed{
		{id: "parent", status: core.Active(), daemon: "daemon-1"},
		{id: "done", status: core.Completed(), workflow: "deploy", title: "Deploy prod"},
		{id: "failed", status: core.Failed(), workflow: "deploy"},
		{id: "paused", status: core.Paused()},
		{id: "queued", noRoot: true},
		{id: "archived", status: core.Completed(), archived: true, workflow: "nightly"},
		{id: "test-run", status: core.Completed(), launch: core.TriggerEventKind("builder.test")},
		{id: "child", status: core.Active(), launch: core.TriggerEventKind("agent.start_run"), parent: "parent"},
		{id: "theirs", status: core.Active(), user: "someone-else"},
	})

	user := storetest.SeedUserID
	after := l2Base.Add(90 * time.Second)
	cursor := &core.RunCursor{CreatedAt: l2Base.Add(5 * time.Minute), ChatID: "archived"}
	shapes := []struct {
		name    string
		filters core.RunListFilters
		want    string
	}{
		{"default", core.RunListFilters{UserID: user, Limit: 50}, "[child queued paused failed done parent]"},
		{"archived included", core.RunListFilters{UserID: user, Limit: 50, IncludeArchived: true}, "[child archived queued paused failed done parent]"},
		{"page of two", core.RunListFilters{UserID: user, Limit: 2}, "[child queued]"},
		{"after a cursor", core.RunListFilters{UserID: user, Limit: 50, After: cursor}, "[queued paused failed done parent]"},
		{"by last active", core.RunListFilters{UserID: user, Limit: 50, ByLastActive: true}, "[parent done failed paused queued child]"},
		{"workflows", core.RunListFilters{UserID: user, Limit: 50, Workflows: []string{"deploy"}}, "[failed done]"},
		{"project", core.RunListFilters{UserID: user, Limit: 50, ProjectID: ptr(storetest.SeedProjectID)}, "[child queued paused failed done parent]"},
		{"other project", core.RunListFilters{UserID: user, Limit: 50, ProjectID: ptr("nope")}, "[]"},
		{"builder tests", core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"builder.test"}}, "[test-run]"},
		{"interactive", core.RunListFilters{UserID: user, Limit: 50, LaunchKinds: []string{"chat.start"}}, "[queued paused failed done parent]"},
		{"display states", core.RunListFilters{UserID: user, Limit: 50, DisplayStates: []core.RunDisplayState{core.RunDisplayFailed, core.RunDisplayQueued}}, "[queued failed]"},
		{"started window", core.RunListFilters{UserID: user, Limit: 50, StartedAfter: &after, StartedBefore: ptr(l2Base.Add(4 * time.Minute))}, "[paused failed]"},
		{"query", core.RunListFilters{UserID: user, Limit: 50, Query: ptr("PROD")}, "[done]"},
		{"children of", core.RunListFilters{UserID: user, Limit: 50, ParentChatID: ptr("parent")}, "[child]"},
		{"trigger", core.RunListFilters{UserID: user, Limit: 50, TriggerID: ptr("trg-none")}, "[]"},
	}
	for _, s := range shapes {
		port, portMore, err := store.ListRuns(ctx, env.ORM, s.filters)
		if err != nil {
			t.Fatalf("%s: port: %v", s.name, err)
		}
		twin, twinMore, err := env.Repo.ListRuns(ctx, s.filters)
		if err != nil {
			t.Fatalf("%s: twin: %v", s.name, err)
		}
		if got := fmt.Sprint(runIDs(port)); got != s.want {
			t.Fatalf("%s: port listed %s, want %s", s.name, got, s.want)
		}
		requireSame(t, s.name, port, twin)
		if portMore != twinMore {
			t.Fatalf("%s: hasMore port %v, twin %v", s.name, portMore, twinMore)
		}
	}
	child, _, _ := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: user, Limit: 1, ParentChatID: ptr("parent")})
	if child[0].ParentChatID != "parent" || child[0].ParentChatTitle != "Run parent" {
		t.Fatalf("child run's parent = %q %q", child[0].ParentChatID, child[0].ParentChatTitle)
	}

	for _, bad := range []core.RunListFilters{
		{Limit: 50},
		{UserID: user},
		{UserID: user, Limit: 5, ByLastActive: true, After: cursor},
	} {
		_, _, portErr := store.ListRuns(ctx, env.ORM, bad)
		_, _, twinErr := env.Repo.ListRuns(ctx, bad)
		if portErr == nil || twinErr == nil || portErr.Error() != twinErr.Error() {
			t.Fatalf("refusal of %+v: port %v, twin %v", bad, portErr, twinErr)
		}
	}

	last := []struct {
		name    string
		filters core.RunListFilters
		want    string
	}{
		{"every workflow", core.RunListFilters{UserID: user}, "[child failed archived]"},
		{"named", core.RunListFilters{UserID: user, Workflows: []string{"deploy"}}, "[failed]"},
		// Only the user, project and workflow filters apply here.
		{"children of is ignored", core.RunListFilters{UserID: user, ParentChatID: ptr("parent")}, "[child failed archived]"},
	}
	for _, s := range last {
		port, err := store.LastRunPerWorkflow(ctx, env.ORM, s.filters)
		if err != nil {
			t.Fatalf("%s: port: %v", s.name, err)
		}
		twin, err := env.Repo.LastRunPerWorkflow(ctx, s.filters)
		if err != nil {
			t.Fatalf("%s: twin: %v", s.name, err)
		}
		if got := fmt.Sprint(runIDs(port)); got != s.want {
			t.Fatalf("last run, %s: port listed %s, want %s", s.name, got, s.want)
		}
		requireSame(t, "last run, "+s.name, port, twin)
	}
}
