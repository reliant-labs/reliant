// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
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
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // fixed: nothing here depends on the wall clock
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

// The backfill stamps in bounded batches, each resuming after the last id the
// previous one stamped, until nothing of the user's is left unassigned; a
// chat takes its project's org whichever batch either was stamped in.
func TestAssignUnassignedOrg_SpansBatches(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	user := storetest.SeedUserID

	// Seven projects with no org and one of acme; two chats in each.
	projects := map[string]*string{"p-acme": orgp("acme")}
	for i := range 7 {
		projects[fmt.Sprintf("p-legacy-%d", i)] = nil
	}
	for id, org := range projects {
		if err := env.Repo.CreateProject(ctx, &core.Project{ID: id, UserID: user, Name: id, Path: "/srv/" + id, OrgID: org,
			CreatedAt: at, UpdatedAt: at, LastActive: at}); err != nil {
			t.Fatalf("CreateProject(%s): %v", id, err)
		}
		for j := range 2 {
			c := &core.Chat{ID: fmt.Sprintf("c-%s-%d", id, j), ProjectID: id, UserID: user, Title: "x",
				CreatedAt: at, UpdatedAt: at, LastActive: at}
			if err := store.InsertChat(ctx, env.ORM, c); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A batch of 3: seven projects take three batches, sixteen chats six.
	if err := store.AssignUnassignedOrgInBatches(ctx, env.ORM, user, "home", 3); err != nil {
		t.Fatalf("AssignUnassignedOrg: %v", err)
	}
	for id, org := range projects {
		want := "home"
		if org != nil {
			want = *org
		}
		got, err := env.Repo.GetProject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.OrgID == nil || *got.OrgID != want {
			t.Fatalf("project %s org = %v, want %s", id, got.OrgID, want)
		}
		for j := range 2 {
			chat, err := env.Repo.GetChatRow(ctx, fmt.Sprintf("c-%s-%d", id, j))
			if err != nil {
				t.Fatal(err)
			}
			if chat.OrgID == nil || *chat.OrgID != want {
				t.Fatalf("chat of %s org = %v, want its project's %s", id, chat.OrgID, want)
			}
		}
	}
}

// A project a concurrent write holds is skipped by a pass, and so are its
// chats: they never take that pass's default while their project may later
// take another. A later pass with a different default stamps the project and
// its chats alike.
func TestAssignUnassignedOrg_ChatsWaitForTheirProject(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	user := storetest.SeedUserID
	for _, id := range []string{"p-held", "p-free"} {
		if err := env.Repo.CreateProject(ctx, &core.Project{ID: id, UserID: user, Name: id, Path: "/srv/" + id,
			CreatedAt: at, UpdatedAt: at, LastActive: at}); err != nil {
			t.Fatal(err)
		}
		for j := range 2 {
			if err := store.InsertChat(ctx, env.ORM, &core.Chat{ID: fmt.Sprintf("c-%s-%d", id, j), ProjectID: id, UserID: user,
				Title: "x", CreatedAt: at, UpdatedAt: at, LastActive: at}); err != nil {
				t.Fatal(err)
			}
		}
	}
	org := func(get func() (*string, error)) string {
		t.Helper()
		got, err := get()
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			return ""
		}
		return *got
	}
	projectOrg := func(id string) string {
		return org(func() (*string, error) {
			p, err := env.Repo.GetProject(ctx, id)
			if err != nil {
				return nil, err
			}
			return p.OrgID, nil
		})
	}
	chatOrg := func(id string) string {
		return org(func() (*string, error) {
			c, err := env.Repo.GetChatRow(ctx, id)
			if err != nil {
				return nil, err
			}
			return c.OrgID, nil
		})
	}

	// Another transaction holds p-held while a pass with default home runs.
	held, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.ExecContext(ctx, `SELECT id FROM projects WHERE id = 'p-held' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignUnassignedOrg(ctx, env.ORM, user, "home"); err != nil {
		t.Fatal(err)
	}
	if got := projectOrg("p-held"); got != "" {
		t.Fatalf("the held project was stamped %q", got)
	}
	for j := range 2 {
		if got := chatOrg(fmt.Sprintf("c-p-held-%d", j)); got != "" {
			t.Fatalf("a chat of the held project took %q before its project", got)
		}
		if got := chatOrg(fmt.Sprintf("c-p-free-%d", j)); got != "home" {
			t.Fatalf("a chat of the free project has %q, want its project's home", got)
		}
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}

	// The default has changed by the next pass: the project and its chats
	// still agree.
	if err := store.AssignUnassignedOrg(ctx, env.ORM, user, "acme"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p-held", "p-free"} {
		want := projectOrg(id)
		if want == "" {
			t.Fatalf("project %s was never stamped", id)
		}
		for j := range 2 {
			if got := chatOrg(fmt.Sprintf("c-%s-%d", id, j)); got != want {
				t.Fatalf("chat %d of %s has %q, its project %q", j, id, got, want)
			}
		}
	}
}
