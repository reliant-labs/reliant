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

// Triggers, saved workflows and runs carry their org too: a run inherits its
// chat's on insert (workflows_inherit_chat_org), lists by org include only
// that org's rows, and the backfill gives a trigger its project's org, a saved
// workflow the default org, and a run its chat's.
func TestOrgOfTriggersSavedWorkflowsAndRuns(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	user := storetest.SeedUserID

	if err := env.Repo.CreateProject(ctx, &core.Project{ID: "p-acme", UserID: user, Name: "acme", Path: "/srv/acme",
		OrgID: orgp("acme"), CreatedAt: at, UpdatedAt: at, LastActive: at}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*core.Chat{
		{ID: "c-acme", ProjectID: "p-acme", OrgID: orgp("acme")},
		{ID: "c-legacy", ProjectID: storetest.SeedProjectID},
	} {
		c.UserID, c.Title, c.CreatedAt, c.UpdatedAt, c.LastActive = user, c.ID, at, at, at
		if err := store.InsertChat(ctx, env.ORM, c); err != nil {
			t.Fatal(err)
		}
	}
	convWorkflow(t, env, "w-acme", "c-acme", core.WorkflowStateActive, core.StopReasonUnspecified)
	convWorkflow(t, env, "w-legacy", "c-legacy", core.WorkflowStateActive, core.StopReasonUnspecified)
	runOrg := func(id string) *string {
		t.Helper()
		w, err := env.Repo.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return w.OrgID
	}
	if org := runOrg("w-acme"); org == nil || *org != "acme" {
		t.Fatalf("a run in an acme chat has org %v, want acme (inherited on insert)", org)
	}
	if org := runOrg("w-legacy"); org != nil {
		t.Fatalf("a run in a chat with no org has org %q, want none", *org)
	}

	acmeTrigger := newStoreTrigger("t-acme", "acme", at)
	acmeTrigger.ProjectID, acmeTrigger.OrgID = "p-acme", orgp("acme")
	legacyTrigger := newStoreTrigger("t-legacy", "legacy", at)
	legacyTrigger.ProjectID = "p-acme" // written before orgs, in a project that has one now
	for _, tr := range []*core.Trigger{acmeTrigger, legacyTrigger} {
		if err := env.Repo.CreateTrigger(ctx, tr); err != nil {
			t.Fatalf("CreateTrigger(%s): %v", tr.ID, err)
		}
	}
	got, err := env.Repo.GetTrigger(ctx, "t-acme")
	if err != nil || got.OrgID == nil || *got.OrgID != "acme" {
		t.Fatalf("GetTrigger org = %v, %v; want acme", got, err)
	}
	triggerIDs := func(scope *core.OrgScope) []string {
		t.Helper()
		ts, err := env.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: user, Org: scope})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, tr := range ts {
			ids = append(ids, tr.ID)
		}
		sort.Strings(ids)
		return ids
	}
	if ids := triggerIDs(&core.OrgScope{OrgID: "acme"}); len(ids) != 1 || ids[0] != "t-acme" {
		t.Fatalf("triggers of acme = %v, want t-acme", ids)
	}
	if ids := triggerIDs(nil); len(ids) != 2 {
		t.Fatalf("triggers, no org = %v, want both", ids)
	}

	runChats := func(scope *core.OrgScope) []string {
		t.Helper()
		items, _, err := store.ListRuns(ctx, env.ORM, core.RunListFilters{UserID: user, Org: scope, Limit: 50, IncludeArchived: true})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ChatID)
		}
		sort.Strings(ids)
		return ids
	}
	if ids := runChats(&core.OrgScope{OrgID: "acme"}); len(ids) != 1 || ids[0] != "c-acme" {
		t.Fatalf("runs of acme = %v, want c-acme", ids)
	}
	if ids := runChats(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}); len(ids) != 1 || ids[0] != "c-legacy" {
		t.Fatalf("runs of home, the default = %v, want the unassigned c-legacy", ids)
	}

	// The run log's unlaunched trigger events are their trigger's org's.
	for _, tr := range []string{"t-acme", "t-legacy"} {
		if _, err := env.SQL.Exec(`INSERT INTO trigger_events (id, user_id, kind, dedupe_key, occurred_at, outcome, trigger_id)
			VALUES ($1, $2, 'schedule', $1, NOW(), 'skipped', $3)`, "ev-"+tr, user, tr); err != nil {
			t.Fatalf("insert skipped event of %s: %v", tr, err)
		}
	}
	eventTriggers := func(scope *core.OrgScope) []string {
		t.Helper()
		evs, _, err := store.ListUnlaunchedTriggerEvents(ctx, env.ORM, core.RunListFilters{UserID: user, Org: scope, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, ev := range evs {
			ids = append(ids, ev.TriggerID)
		}
		sort.Strings(ids)
		return ids
	}
	if ids := eventTriggers(&core.OrgScope{OrgID: "acme"}); len(ids) != 1 || ids[0] != "t-acme" {
		t.Fatalf("unlaunched events of acme = %v, want t-acme's only", ids)
	}
	if ids := eventTriggers(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}); len(ids) != 1 || ids[0] != "t-legacy" {
		t.Fatalf("unlaunched events of home, the default = %v, want the unassigned t-legacy's", ids)
	}
	if ids := eventTriggers(nil); len(ids) != 2 {
		t.Fatalf("unlaunched events, no org = %v, want both", ids)
	}

	if err := env.Repo.CreateWorkflowDraft(ctx, &core.WorkflowDraft{ID: "d-legacy", UserID: user, Name: "d", Slug: "d",
		Definition: "name: d", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}

	if err := env.Repo.AssignUnassignedOrg(ctx, user, "home"); err != nil {
		t.Fatalf("AssignUnassignedOrg: %v", err)
	}
	if tr, _ := env.Repo.GetTrigger(ctx, "t-legacy"); tr.OrgID == nil || *tr.OrgID != "acme" {
		t.Fatalf("legacy trigger org = %v, want its project's (acme)", tr.OrgID)
	}
	if d, _ := env.Repo.GetWorkflowDraft(ctx, "d-legacy"); d.OrgID == nil || *d.OrgID != "home" {
		t.Fatalf("legacy saved workflow org = %v, want the default org", d.OrgID)
	}
	if org := runOrg("w-legacy"); org == nil || *org != "home" {
		t.Fatalf("legacy run org = %v, want its chat's stamped org (home)", org)
	}
	if org := runOrg("w-acme"); org == nil || *org != "acme" {
		t.Fatalf("an assigned run moved to %v", org)
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
