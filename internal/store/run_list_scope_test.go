// Copyright (c) 2026 Reliant Labs
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

// Both run lists — the run log and the newest run per workflow (the
// Workflows list's "Last run") — honour the same scope: one org (with the
// caller's unassigned runs in their default org), and one project or the
// union of several (the side nav's choice of several projects).
func TestRunListsScopeByOrgAndProjectSet(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // fixed: nothing here depends on the wall clock
	user := storetest.SeedUserID

	for _, p := range []*core.Project{
		{ID: "p-acme", OrgID: orgp("acme")},
		{ID: "p-acme2", OrgID: orgp("acme")},
		{ID: "p-home", OrgID: orgp("home")},
	} {
		p.UserID, p.Name, p.Path, p.CreatedAt, p.UpdatedAt, p.LastActive = user, p.ID, "/srv/"+p.ID, at, at, at
		if err := env.Repo.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// c-home is wf's newest run of all, in another org than acme's.
	for i, c := range []struct{ id, project, workflow string }{
		{"c-acme", "p-acme", "wf"},
		{"c-acme2", "p-acme2", "wf2"},
		{"c-legacy", storetest.SeedProjectID, "wf3"},
		{"c-home", "p-home", "wf"},
	} {
		workflow := c.workflow
		created := at.Add(time.Duration(i) * time.Minute)
		chat := &core.Chat{ID: c.id, ProjectID: c.project, UserID: user, Title: c.id, WorkflowName: &workflow,
			CreatedAt: created, UpdatedAt: created, LastActive: created}
		switch c.project {
		case "p-acme", "p-acme2":
			chat.OrgID = orgp("acme")
		case "p-home":
			chat.OrgID = orgp("home")
		}
		if err := store.InsertChat(ctx, env.ORM, chat); err != nil {
			t.Fatal(err)
		}
	}

	lastRuns := func(f core.RunListFilters) map[string]string {
		t.Helper()
		f.UserID = user
		items, err := store.LastRunPerWorkflow(ctx, env.ORM, f)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, it := range items {
			got[it.WorkflowName] = it.ChatID
		}
		return got
	}
	runs := func(f core.RunListFilters) []string {
		t.Helper()
		f.UserID, f.Limit = user, 50
		items, _, err := store.ListRuns(ctx, env.ORM, f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ChatID)
		}
		return ids
	}
	same := func(name string, got, want any) {
		t.Helper()
		if gotS, wantS := fmt.Sprint(got), fmt.Sprint(want); gotS != wantS {
			t.Fatalf("%s = %s, want %s", name, gotS, wantS)
		}
	}

	same("last runs of acme", lastRuns(core.RunListFilters{Org: &core.OrgScope{OrgID: "acme"}}),
		map[string]string{"wf": "c-acme", "wf2": "c-acme2"})
	same("last runs of home, the default", lastRuns(core.RunListFilters{Org: &core.OrgScope{OrgID: "home", IncludeUnassigned: true}}),
		map[string]string{"wf": "c-home", "wf3": "c-legacy"})
	same("last runs, no org", lastRuns(core.RunListFilters{}),
		map[string]string{"wf": "c-home", "wf2": "c-acme2", "wf3": "c-legacy"})
	same("last runs of two projects", lastRuns(core.RunListFilters{ProjectIDs: []string{"p-acme", "p-acme2"}}),
		map[string]string{"wf": "c-acme", "wf2": "c-acme2"})

	same("runs of two projects", runs(core.RunListFilters{ProjectIDs: []string{"p-acme", "p-home"}}), []string{"c-home", "c-acme"})
	same("runs of project_id with project_ids", runs(core.RunListFilters{ProjectID: orgp("p-acme2"), ProjectIDs: []string{"p-home"}}),
		[]string{"c-home", "c-acme2"})
	same("runs of one project", runs(core.RunListFilters{ProjectIDs: []string{"p-acme"}}), []string{"c-acme"})
	same("runs of two projects in acme", runs(core.RunListFilters{Org: &core.OrgScope{OrgID: "acme"}, ProjectIDs: []string{"p-acme", "p-home"}}),
		[]string{"c-acme"})
}
