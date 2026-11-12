// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Read-only sharing's store half (control-plane docs/design/org-sharing.md
// §9, PR S1).

// shareChat writes a chat owned by owner in org (nil: none), last active at.
func shareChat(t *testing.T, env *storetest.Env, id, owner string, org *string, at time.Time) *core.Chat {
	t.Helper()
	c := &core.Chat{
		ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: owner, OrgID: org,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}
	if err := store.InsertChat(context.Background(), env.ORM, c); err != nil {
		t.Fatalf("insert chat %s: %v", id, err)
	}
	return c
}

func share(t *testing.T, env *storetest.Env, kind core.ShareKind, id, owner, org string) {
	t.Helper()
	changed, err := env.Repo.ApplyShareChange(context.Background(), core.ShareChange{
		Kind: kind, ResourceID: id, OrgID: org, OwnerUserID: owner, ActorUserID: owner, Action: core.ShareActionShare,
	})
	if err != nil || !changed {
		t.Fatalf("share %s %s: changed=%v err=%v", kind, id, changed, err)
	}
}

// The shared list holds exactly the org's shared chats the viewer did not
// create: never the caller's own, another org's, a private one, or an
// archived one; most recently active first; narrowed by project on request.
func TestListSharedChats(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	acme, globex := strp("acme"), strp("globex")
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	other := &core.Project{ID: "proj-other", Name: "other", Path: "/srv/other", UserID: "bob", OrgID: acme, CreatedAt: base, UpdatedAt: base, LastActive: base}
	if err := store.InsertProject(ctx, env.ORM, other); err != nil {
		t.Fatal(err)
	}

	shareChat(t, env, "bob-shared-old", "bob", acme, base)
	shareChat(t, env, "bob-shared-new", "bob", acme, base.Add(time.Hour))
	shareChat(t, env, "carol-shared", "carol", acme, base.Add(30*time.Minute))
	shareChat(t, env, "alice-own-shared", "alice", acme, base.Add(2*time.Hour))
	shareChat(t, env, "bob-private", "bob", acme, base.Add(3*time.Hour))
	shareChat(t, env, "bob-globex-shared", "bob", globex, base.Add(4*time.Hour))
	shareChat(t, env, "bob-archived-shared", "bob", acme, base.Add(5*time.Hour))
	inOther := shareChat(t, env, "bob-other-project", "bob", acme, base.Add(10*time.Minute))
	inOther.ProjectID = other.ID
	if err := store.SaveChat(ctx, env.ORM, inOther); err != nil {
		t.Fatal(err)
	}
	for id, owner := range map[string]string{
		"bob-shared-old": "bob", "bob-shared-new": "bob", "carol-shared": "carol", "alice-own-shared": "alice",
		"bob-archived-shared": "bob", "bob-other-project": "bob",
	} {
		share(t, env, core.ShareKindChat, id, owner, "acme")
	}
	share(t, env, core.ShareKindChat, "bob-globex-shared", "bob", "globex")
	if err := store.UpdateChatState(ctx, env.ORM, "bob-archived-shared", core.ChatStateArchived, nil, base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := env.Repo.ListSharedChats(ctx, core.SharedChatFilters{OrgID: "acme", ViewerUserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bob-shared-new", "carol-shared", "bob-other-project", "bob-shared-old"}
	if ids := convChatIDs(got); !reflect.DeepEqual(ids, want) {
		t.Fatalf("shared chats = %v, want %v", ids, want)
	}
	for _, c := range got {
		if c.Visibility != core.VisibilityOrg || c.SharedAt == nil {
			t.Fatalf("%s: visibility %q shared_at %v, want org and a time", c.ID, c.Visibility, c.SharedAt)
		}
	}

	byProject, err := env.Repo.ListSharedChats(ctx, core.SharedChatFilters{OrgID: "acme", ViewerUserID: "alice", ProjectID: strp(other.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if ids := convChatIDs(byProject); !reflect.DeepEqual(ids, []string{"bob-other-project"}) {
		t.Fatalf("one project's shared chats = %v", ids)
	}

	// bob sees carol's and alice's, never his own.
	bobs, err := env.Repo.ListSharedChats(ctx, core.SharedChatFilters{OrgID: "acme", ViewerUserID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if ids := convChatIDs(bobs); !reflect.DeepEqual(ids, []string{"alice-own-shared", "carol-shared"}) {
		t.Fatalf("bob's shared list = %v", ids)
	}

	paged, err := env.Repo.ListSharedChats(ctx, core.SharedChatFilters{OrgID: "acme", ViewerUserID: "alice", Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ids := convChatIDs(paged); !reflect.DeepEqual(ids, []string{"carol-shared", "bob-other-project"}) {
		t.Fatalf("page = %v", ids)
	}

	for name, f := range map[string]core.SharedChatFilters{
		"no org":    {ViewerUserID: "alice"},
		"no viewer": {OrgID: "acme"},
	} {
		if got, err := env.Repo.ListSharedChats(ctx, f); err != nil || len(got) != 0 {
			t.Fatalf("%s: %v, %v; want nothing", name, convChatIDs(got), err)
		}
	}

	// A private chat reads back private through both reads.
	if c, err := store.GetChat(ctx, env.ORM, "bob-private"); err != nil || c.Visibility != core.VisibilityPrivate || c.SharedAt != nil {
		t.Fatalf("private chat via view: %+v, %v", c, err)
	}
	if r, err := store.GetChatRow(ctx, env.ORM, "bob-shared-new"); err != nil || r.Visibility != core.VisibilityOrg || r.SharedAt == nil {
		t.Fatalf("shared chat row: %+v, %v", r, err)
	}
}

// The same predicate over projects.
func TestListSharedProjects(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	add := func(id, owner string, org *string, at time.Time) {
		t.Helper()
		p := &core.Project{ID: id, Name: id, Path: "/srv/" + id, UserID: owner, OrgID: org, CreatedAt: at, UpdatedAt: at, LastActive: at}
		if err := store.InsertProject(ctx, env.ORM, p); err != nil {
			t.Fatal(err)
		}
	}
	add("bob-shared", "bob", strp("acme"), base)
	add("carol-shared", "carol", strp("acme"), base.Add(time.Hour))
	add("alice-shared", "alice", strp("acme"), base.Add(2*time.Hour))
	add("bob-private", "bob", strp("acme"), base.Add(3*time.Hour))
	add("bob-globex", "bob", strp("globex"), base.Add(4*time.Hour))
	share(t, env, core.ShareKindProject, "bob-shared", "bob", "acme")
	share(t, env, core.ShareKindProject, "carol-shared", "carol", "acme")
	share(t, env, core.ShareKindProject, "alice-shared", "alice", "acme")
	share(t, env, core.ShareKindProject, "bob-globex", "bob", "globex")

	got, err := env.Repo.ListSharedProjects(ctx, core.SharedProjectFilters{OrgID: "acme", ViewerUserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
		if p.Visibility != core.VisibilityOrg || p.SharedAt == nil {
			t.Fatalf("%s: visibility %q shared_at %v", p.ID, p.Visibility, p.SharedAt)
		}
	}
	if want := []string{"carol-shared", "bob-shared"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("shared projects = %v, want %v", ids, want)
	}
	if got, err := env.Repo.ListSharedProjects(ctx, core.SharedProjectFilters{OrgID: "acme"}); err != nil || len(got) != 0 {
		t.Fatalf("no viewer: %d rows, %v", len(got), err)
	}
}

// ApplyShareChange changes visibility and writes exactly one share_events row
// per real change; a no-op, a stale owner or org, and a missing row write
// neither. It moves nothing else on the row: changing who may read a chat is
// not activity on it.
func TestApplyShareChange(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "c1", "bob", strp("acme"), at)
	if err := store.UpdateChatUnread(ctx, env.ORM, "c1", true); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetChatRow(ctx, env.ORM, "c1")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(c core.ShareChange) bool {
		t.Helper()
		c.Kind, c.ResourceID = core.ShareKindChat, "c1"
		if c.OrgID == "" {
			c.OrgID = "acme"
		}
		if c.OwnerUserID == "" {
			c.OwnerUserID = "bob"
		}
		if c.ActorUserID == "" {
			c.ActorUserID = c.OwnerUserID
		}
		changed, err := env.Repo.ApplyShareChange(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	events := func() []*core.ShareEvent {
		t.Helper()
		ev, err := env.Repo.ListShareEvents(ctx, core.ShareKindChat, "c1")
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	sharedAt := at.Add(time.Hour)
	if !apply(core.ShareChange{Action: core.ShareActionShare, At: sharedAt}) {
		t.Fatal("share: not applied")
	}
	if apply(core.ShareChange{Action: core.ShareActionShare}) {
		t.Fatal("sharing a shared chat applied")
	}
	if apply(core.ShareChange{Action: core.ShareActionUnshare, OwnerUserID: "mallory"}) {
		t.Fatal("an unshare naming another owner applied")
	}
	if apply(core.ShareChange{Action: core.ShareActionUnshare, OrgID: "globex"}) {
		t.Fatal("an unshare naming another org applied")
	}
	row, err := store.GetChatRow(ctx, env.ORM, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Visibility != core.VisibilityOrg || row.SharedAt == nil || !row.SharedAt.Equal(sharedAt) {
		t.Fatalf("after share: visibility %q shared_at %v", row.Visibility, row.SharedAt)
	}
	if !row.Unread || !row.LastActive.Equal(before.LastActive) || !row.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("share moved the row: unread %v last_active %v updated_at %v", row.Unread, row.LastActive, row.UpdatedAt)
	}
	if ev := events(); len(ev) != 1 || ev[0].Action != core.ShareActionShare || ev[0].ActorUserID != "bob" || ev[0].OwnerUserID != "bob" || ev[0].OrgID != "acme" || !ev[0].CreatedAt.Equal(sharedAt) {
		t.Fatalf("after share: events %+v", ev)
	}

	if !apply(core.ShareChange{Action: core.ShareActionAdminUnshare, ActorUserID: "adam"}) {
		t.Fatal("admin unshare: not applied")
	}
	if apply(core.ShareChange{Action: core.ShareActionUnshare}) {
		t.Fatal("unsharing a private chat applied")
	}
	row, err = store.GetChatRow(ctx, env.ORM, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Visibility != core.VisibilityPrivate || row.SharedAt != nil {
		t.Fatalf("after admin unshare: visibility %q shared_at %v", row.Visibility, row.SharedAt)
	}
	ev := events()
	if len(ev) != 2 || ev[0].Action != core.ShareActionAdminUnshare || ev[0].ActorUserID != "adam" || ev[0].OwnerUserID != "bob" {
		t.Fatalf("after admin unshare: events %+v", ev)
	}

	if !apply(core.ShareChange{Action: core.ShareActionShare}) || !apply(core.ShareChange{Action: core.ShareActionUnshare}) {
		t.Fatal("share then unshare: not applied")
	}
	if n := len(events()); n != 4 {
		t.Fatalf("%d events after four changes, want 4", n)
	}

	// A missing row changes nothing.
	if changed, err := env.Repo.ApplyShareChange(ctx, core.ShareChange{
		Kind: core.ShareKindChat, ResourceID: "nope", OrgID: "acme", OwnerUserID: "bob", ActorUserID: "bob", Action: core.ShareActionShare,
	}); err != nil || changed {
		t.Fatalf("missing chat: changed=%v err=%v", changed, err)
	}
	// An unknown kind or action is an error, never interpolated.
	for _, c := range []core.ShareChange{
		{Kind: "users", ResourceID: "c1", OrgID: "acme", OwnerUserID: "bob", ActorUserID: "bob", Action: core.ShareActionShare},
		{Kind: core.ShareKindChat, ResourceID: "c1", OrgID: "acme", OwnerUserID: "bob", ActorUserID: "bob", Action: "publish"},
		{Kind: core.ShareKindChat, ResourceID: "c1", OwnerUserID: "bob", ActorUserID: "bob", Action: core.ShareActionShare},
	} {
		if _, err := env.Repo.ApplyShareChange(ctx, c); err == nil {
			t.Fatalf("%+v: no error", c)
		}
	}
}

// A row with no org cannot be shared: the change does not match it, and the
// database refuses a visibility without an org or outside the set.
func TestShareNeedsAnOrg(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "orgless", "bob", nil, at)
	shareChat(t, env, "acme", "bob", strp("acme"), at)
	for _, stmt := range []string{
		`UPDATE chats SET visibility = 'org' WHERE id = 'orgless'`,
		`UPDATE chats SET visibility = 'public' WHERE id = 'acme'`,
		`UPDATE chats SET visibility = '' WHERE id = 'acme'`,
		`UPDATE projects SET visibility = 'everyone' WHERE id = '` + storetest.SeedProjectID + `'`,
	} {
		if _, err := env.SQL.ExecContext(ctx, stmt); err == nil {
			t.Fatalf("%s: accepted", stmt)
		}
	}
}

// A saved workflow shares the same way and reads its visibility back.
func TestShareWorkflowDraft(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	d := &core.WorkflowDraft{ID: "wf1", UserID: "bob", OrgID: strp("acme"), Name: "Deploy", Slug: "deploy", Definition: "name: deploy", CreatedAt: at, UpdatedAt: at}
	if err := store.InsertWorkflowDraft(ctx, env.ORM, d); err != nil {
		t.Fatal(err)
	}
	share(t, env, core.ShareKindWorkflow, "wf1", "bob", "acme")
	got, err := store.LoadWorkflowDraft(ctx, env.ORM, "wf1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != core.VisibilityOrg || got.SharedAt == nil || got.Version != 1 {
		t.Fatalf("draft: visibility %q shared_at %v version %d", got.Visibility, got.SharedAt, got.Version)
	}
}
