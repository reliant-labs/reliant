// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func orgp(s string) *string { return &s }

// Projects and chats carry their org through every read, a list naming an
// org lists only that org's rows (plus, for the caller's default org, the
// rows with none yet), and the backfill fills only NULL, giving a chat its
// project's org when the project has one.
func TestOrgScopedListsAndBackfill(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	user := storetest.SeedUserID

	for _, p := range []*core.Project{
		{ID: "p-acme", UserID: user, Name: "acme", Path: "/srv/acme", OrgID: orgp("acme"), CreatedAt: at, UpdatedAt: at, LastActive: at},
		{ID: "p-home", UserID: user, Name: "home", Path: "/srv/home", OrgID: orgp("home"), CreatedAt: at, UpdatedAt: at, LastActive: at.Add(time.Second)},
		{ID: "p-legacy", UserID: user, Name: "legacy", Path: "/srv/legacy", CreatedAt: at, UpdatedAt: at, LastActive: at.Add(2 * time.Second)},
	} {
		if err := env.Repo.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", p.ID, err)
		}
	}
	for _, c := range []*core.Chat{
		{ID: "c-acme", ProjectID: "p-acme", OrgID: orgp("acme")},
		{ID: "c-home", ProjectID: "p-home", OrgID: orgp("home")},
		{ID: "c-legacy", ProjectID: "p-legacy"},
		// Created with no org (no org could be resolved) in a project that has one.
		{ID: "c-unresolved", ProjectID: "p-acme"},
	} {
		c.UserID, c.Title, c.CreatedAt, c.UpdatedAt, c.LastActive = user, c.ID, at, at, at
		if err := store.InsertChat(ctx, env.ORM, c); err != nil {
			t.Fatalf("InsertChat(%s): %v", c.ID, err)
		}
	}

	if got, err := env.Repo.GetProject(ctx, "p-acme"); err != nil || got.OrgID == nil || *got.OrgID != "acme" {
		t.Fatalf("GetProject org = %v, %v; want acme", got.OrgID, err)
	}
	if got, err := env.Repo.GetChatRow(ctx, "c-acme"); err != nil || got.OrgID == nil || *got.OrgID != "acme" {
		t.Fatalf("GetChatRow org = %v, %v; want acme", got, err)
	}
	if got, err := env.Repo.GetChat(ctx, "c-home"); err != nil || got.OrgID == nil || *got.OrgID != "home" {
		t.Fatalf("GetChat (the view) org = %v, %v; want home", got, err)
	}

	projectIDs := func(scope *core.OrgScope) []string {
		t.Helper()
		ps, err := env.Repo.ListProjects(ctx, core.ProjectFilters{UserID: user, Org: scope})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, p := range ps {
			if p.ID != storetest.SeedProjectID {
				ids = append(ids, p.ID)
			}
		}
		sort.Strings(ids)
		return ids
	}
	chatIDs := func(scope *core.OrgScope) []string {
		t.Helper()
		cs, err := env.Repo.ListChats(ctx, core.ChatFilters{UserID: user, Org: scope, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		ids := convChatIDs(cs)
		sort.Strings(ids)
		return ids
	}
	equal := func(label string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", label, got, want)
			}
		}
	}

	equal("projects, no org", projectIDs(nil), "p-acme", "p-home", "p-legacy")
	equal("projects of acme", projectIDs(&core.OrgScope{OrgID: "acme"}), "p-acme")
	equal("projects of home, the default", projectIDs(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}), "p-home", "p-legacy")
	equal("chats, no org", chatIDs(nil), "c-acme", "c-home", "c-legacy", "c-unresolved")
	equal("chats of acme", chatIDs(&core.OrgScope{OrgID: "acme"}), "c-acme")
	equal("chats of home, the default", chatIDs(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}), "c-home", "c-legacy", "c-unresolved")

	// Another user's rows are never stamped.
	if err := store.InsertChat(ctx, env.ORM, &core.Chat{ID: "c-other", ProjectID: "p-legacy", UserID: "other-user",
		Title: "x", CreatedAt: at, UpdatedAt: at, LastActive: at}); err != nil {
		t.Fatal(err)
	}

	if err := env.Repo.AssignUnassignedOrg(ctx, user, "home"); err != nil {
		t.Fatalf("AssignUnassignedOrg: %v", err)
	}
	if err := env.Repo.AssignUnassignedOrg(ctx, user, "home"); err != nil {
		t.Fatalf("AssignUnassignedOrg is idempotent: %v", err)
	}
	wantOrg := map[string]string{"c-acme": "acme", "c-home": "home", "c-legacy": "home", "c-unresolved": "acme", "c-other": ""}
	for id, want := range wantOrg {
		got, err := env.Repo.GetChatRow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (got.OrgID == nil && want != "") || (got.OrgID != nil && *got.OrgID != want) {
			t.Fatalf("%s org = %v, want %q", id, got.OrgID, want)
		}
	}
	if got, _ := env.Repo.GetProject(ctx, "p-legacy"); got.OrgID == nil || *got.OrgID != "home" {
		t.Fatalf("p-legacy org = %v, want the default org", got.OrgID)
	}
	equal("chats of home, strictly", chatIDs(&core.OrgScope{OrgID: "home"}), "c-home", "c-legacy")
	equal("chats of acme after the stamp", chatIDs(&core.OrgScope{OrgID: "acme"}), "c-acme", "c-unresolved")

	// The CHECK keeps '' out.
	if err := store.InsertChat(ctx, env.ORM, &core.Chat{ID: "c-blank", ProjectID: "p-legacy", UserID: user, OrgID: orgp(""),
		Title: "x", CreatedAt: at, UpdatedAt: at, LastActive: at}); err == nil {
		t.Fatal("a blank org_id was stored")
	}
}
