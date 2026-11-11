// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// memberOf is a Membership in which user-1 is a live member of exactly the
// named orgs, and whose default org is "home".
type memberOf map[string]bool

func (m memberOf) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

func (memberOf) DefaultOrg(context.Context, string) (string, error) { return "home", nil }

// ListInbox leaves out every item about a chat or project the caller may no
// longer read: a removed member's left org, and the other org of a
// credential bound to one. A recovery whose project is gone stays listed for
// its owner.
func TestListInbox_LeavesOutItemsOfAnOrgTheCallerMayNotRead(t *testing.T) {
	org := func(s string) *string { return &s }
	chatOrgs := map[string]*string{"c-acme": org("acme"), "c-home": org("home")}
	projectOrgs := map[string]*string{"p-acme": org("acme"), "p-home": org("home")}
	newSvc := func() *Service {
		repo := newInflightRepo(0)
		repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "user-1", OrgID: chatOrgs[id]}, nil
		}
		repo.GetProjectFunc = func(_ context.Context, id string) (*db.Project, error) {
			if id == "p-gone" {
				return nil, svcerr.NotFound("project")
			}
			return &db.Project{ID: id, UserID: "user-1", OrgID: projectOrgs[id]}, nil
		}
		repo.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
			now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
			return []*core.InboxPending{
				{Kind: core.InboxPendingApproval, ItemKey: "chat-acme", ChatID: "c-acme", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "chat-home", ChatID: "c-home", ProjectID: "p-home", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "run-acme", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "run-home", ProjectID: "p-home", WaitingSince: now},
			}, nil
		}
		repo.ListWorktreeRecoveriesFunc = func(context.Context, string, string) ([]*core.WorktreeRecovery, error) {
			now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
			return []*core.WorktreeRecovery{
				{ID: "acme", ProjectID: "p-acme", Phase: "held", CreatedAt: now},
				{ID: "home", ProjectID: "p-home", Phase: "held", CreatedAt: now},
				{ID: "gone", ProjectID: "p-gone", Phase: "held", CreatedAt: now},
			}, nil
		}
		return NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	}
	itemIDs := func(ctx context.Context) []string {
		t.Helper()
		resp, err := newSvc().ListInbox(ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
		require.NoError(t, err)
		var out []string
		for _, it := range resp.Msg.Items {
			out = append(out, it.ItemId)
		}
		sort.Strings(out)
		return out
	}
	want := []string{
		inboxApprovalPrefix + "chat-home",
		inboxApprovalPrefix + "run-home",
		"recovery:gone",
		"recovery:home",
	}
	sort.Strings(want)

	removed := authz.WithMembership(userCtx(), memberOf{"home": true})
	require.Equal(t, want, itemIDs(removed), "a removed member of acme")

	both := authz.WithMembership(userCtx(), memberOf{"home": true, "acme": true})
	require.Equal(t, want, itemIDs(authz.WithCredentialOrg(both, "home")), "a credential bound to home, the default org")

	// A recovery whose project is gone is a row of no org: a credential
	// bound to an org other than the owner's default does not reach it.
	acmeOnly := []string{inboxApprovalPrefix + "chat-acme", inboxApprovalPrefix + "run-acme", "recovery:acme"}
	sort.Strings(acmeOnly)
	require.Equal(t, acmeOnly, itemIDs(authz.WithCredentialOrg(both, "acme")), "a credential bound to acme, not the default org")
}

// acmeAndHome is the org source of a live member of acme and home, whose
// default org is home.
type acmeAndHome struct{}

func (acmeAndHome) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// ListInbox names an org like the other lists (orgs.ListScope): only the
// items about its chats, projects and automations, plus, in the default org,
// those about something with no org yet (a legacy chat, a recovery whose
// project is gone). Naming none lists every org's items, as before org_id;
// naming an org the caller is not in is refused.
func TestListInbox_ScopedToTheNamedOrg(t *testing.T) {
	org := func(s string) *string { return &s }
	chatOrgs := map[string]*string{"c-acme": org("acme"), "c-home": org("home"), "c-legacy": nil}
	resolver := orgs.New(acmeAndHome{})
	newSvc := func() *Service {
		repo := newInflightRepo(0)
		repo.AssignUnassignedOrgFunc = func(context.Context, string, string) error { return nil }
		repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "user-1", OrgID: chatOrgs[id]}, nil
		}
		repo.GetProjectFunc = func(_ context.Context, id string) (*db.Project, error) {
			if id == "p-gone" {
				return nil, svcerr.NotFound("project")
			}
			return &db.Project{ID: id, UserID: "user-1", OrgID: org("acme")}, nil
		}
		repo.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
			now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
			return []*core.InboxPending{
				{Kind: core.InboxPendingApproval, ItemKey: "chat-acme", ChatID: "c-acme", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "chat-home", ChatID: "c-home", ProjectID: "p-home", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "chat-legacy", ChatID: "c-legacy", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "run-acme", ProjectID: "p-acme", WaitingSince: now},
				// A trigger's item whose trigger is not among those listed:
				// its project places it.
				{Kind: core.InboxPendingApproval, ItemKey: "trigger-only", TriggerID: "unlisted", ProjectID: "p-acme", WaitingSince: now},
			}, nil
		}
		repo.ListWorktreeRecoveriesFunc = func(context.Context, string, string) ([]*core.WorktreeRecovery, error) {
			now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
			return []*core.WorktreeRecovery{{ID: "gone", ProjectID: "p-gone", Phase: "held", CreatedAt: now}}, nil
		}
		return NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Orgs: resolver}))
	}
	ctx := authz.WithMembership(userCtx(), memberOf{"home": true, "acme": true})
	itemIDs := func(orgID *string) []string {
		t.Helper()
		resp, err := newSvc().ListInbox(ctx, connect.NewRequest(&reliantv1.ListInboxRequest{OrgId: orgID}))
		require.NoError(t, err)
		out := []string{}
		for _, it := range resp.Msg.Items {
			out = append(out, it.ItemId)
		}
		sort.Strings(out)
		return out
	}
	sorted := func(s ...string) []string { sort.Strings(s); return s }
	approval := func(key string) string { return inboxApprovalPrefix + key }

	require.Equal(t, sorted(approval("chat-acme"), approval("chat-home"), approval("chat-legacy"), approval("run-acme"), approval("trigger-only"), "recovery:gone"),
		itemIDs(nil), "no org named: every org's, as before")
	require.Equal(t, sorted(approval("chat-acme"), approval("run-acme"), approval("trigger-only")), itemIDs(ptr.Of("acme")), "acme")
	require.Equal(t, sorted(approval("chat-home"), approval("chat-legacy"), "recovery:gone"), itemIDs(ptr.Of("home")),
		"home, the default org, with the items of no org")

	_, err := newSvc().ListInbox(ctx, connect.NewRequest(&reliantv1.ListInboxRequest{OrgId: ptr.Of("globex")}))
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "an org the caller is not in")
}

// An item is judged by its chat, else its automation, else its project,
// whichever of them was read; one naming something whose org nobody read is
// undecided (left out of every named org), and a machine's storage item is
// about no org and lists in every one.
func TestInboxReads_ItemOrg(t *testing.T) {
	acme, home := "acme", "home"
	r := &inboxReads{orgs: map[string]*string{"chat:c": &acme, "trigger:t": &home, "project:p": &acme, "project:legacy": nil}}
	cases := []struct {
		it     *reliantv1.InboxItem
		org    *string
		placed itemPlacement
	}{
		{&reliantv1.InboxItem{ChatId: "c", TriggerId: "t", ProjectId: "p"}, &acme, placedInOrg},
		{&reliantv1.InboxItem{TriggerId: "t", ProjectId: "p"}, &home, placedInOrg},
		{&reliantv1.InboxItem{ProjectId: "p"}, &acme, placedInOrg},
		{&reliantv1.InboxItem{ProjectId: "legacy"}, nil, placedInOrg},
		// A key nobody read falls through to the next one.
		{&reliantv1.InboxItem{ChatId: "unread", TriggerId: "unlisted", ProjectId: "p"}, &acme, placedInOrg},
		{&reliantv1.InboxItem{ProjectId: "unread"}, nil, placedUnknown},
		{&reliantv1.InboxItem{Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE}, nil, placedEveryOrg},
		{&reliantv1.InboxItem{Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM, ProjectId: "unread"}, nil, placedEveryOrg},
	}
	for _, c := range cases {
		org, placed := r.itemOrg(c.it)
		require.Equal(t, c.placed, placed, "%v", c.it)
		require.Equal(t, c.org, org, "%v", c.it)
	}
}

// Under an org that is not the caller's default (IncludeUnassigned false),
// a machine's items still list (they are about no org's resource, so every
// org's), while an item of no org yet (it belongs to the default org) and one
// whose org could not be read do not. The default org lists the machine's
// items and the item of no org, never the unknown one.
func TestInboxReads_InScopeKeepsMachineItemsInEveryOrg(t *testing.T) {
	acme := "acme"
	r := &inboxReads{orgs: map[string]*string{"project:p-acme": &acme, "project:p-legacy": nil}}
	items := func() []*reliantv1.InboxItem {
		return []*reliantv1.InboxItem{
			{ItemId: "storage", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE},
			{ItemId: "reclaim", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM, ProjectId: "p-acme"},
			{ItemId: "acme", ProjectId: "p-acme"},
			{ItemId: "legacy", ProjectId: "p-legacy"},
			{ItemId: "unknown", ProjectId: "p-unread"},
		}
	}
	ids := func(scope *core.OrgScope) []string {
		var out []string
		for _, it := range r.inScope(items(), scope) {
			out = append(out, it.ItemId)
		}
		return out
	}
	require.Equal(t, []string{"storage", "reclaim", "acme"}, ids(&core.OrgScope{OrgID: "acme"}), "a non-default org")
	require.Equal(t, []string{"storage", "reclaim"}, ids(&core.OrgScope{OrgID: "globex"}), "another non-default org")
	require.Equal(t, []string{"storage", "reclaim", "legacy"}, ids(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}), "the default org")
	require.Equal(t, []string{"storage", "reclaim", "acme", "legacy", "unknown"}, ids(nil), "no org named")
}

// A broken workflow file's item carries only its project: its org is read
// from the project, so it lists under that org (acme) and not under the
// caller's default (home), and with no org named, as before.
func TestListInbox_BrokenDefinitionListsUnderItsProjectsOrg(t *testing.T) {
	f := newInboxFixture(t)
	f.inbox = NewTestInbox(t, WithInboxDeps(Deps{Repo: f.repo, Orgs: orgs.New(acmeAndHome{})}))
	ctx := authz.WithMembership(f.ctx, memberOf{"home": true, "acme": true})
	_, err := f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = 'acme' WHERE id = ?`, f.project()).Exec(ctx)
	require.NoError(t, err)

	cicdID := uuid.NewString()
	host := "cicd"
	require.NoError(t, f.repo.UpsertDaemon(ctx, &db.Daemon{ID: cicdID, UserID: f.userID, Hostname: &host}))
	push := func(at int64, content string) {
		t.Helper()
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: ".reliant/workflows/release.yaml", YAMLContent: content}})
		require.NoError(t, err)
		sj, err := config.EncodeCatalogJSON([]config.SyncedRepoState{{Repo: "app", Branch: "main", Commit: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b", DefaultBranch: "main"}})
		require.NoError(t, err)
		_, err = f.repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: f.project(), DaemonID: cicdID, ProjectWorkflowsJSON: wj, RepoStatesJSON: sj, PushedAt: time.UnixMilli(at).UTC(),
		}, false)
		require.NoError(t, err)
	}
	push(1000, releaseWorkflow)
	now := time.Now().UTC().Truncate(time.Microsecond)
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	declared := "merged"
	acme := "acme"
	require.NoError(t, f.repo.CreateTrigger(ctx, &core.Trigger{
		ID: "act-" + uuid.NewString()[:8], UserID: f.userID, ProjectID: f.project(), OrgID: &acme, Name: "Release",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "release", WorkflowTrigger: &declared,
		DaemonID: cicdID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg, CreatedAt: now, UpdatedAt: now,
	}))
	push(2000, "name: release\nentry: [a\n")

	broken := func(orgID *string) int {
		t.Helper()
		f.inbox.storageCache.Delete(f.userID)
		resp, err := f.inbox.ListInbox(ctx, connect.NewRequest(&reliantv1.ListInboxRequest{OrgId: orgID}))
		require.NoError(t, err)
		return len(inboxByKind(resp.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN))
	}
	require.Equal(t, 1, broken(nil), "no org named")
	require.Equal(t, 1, broken(ptr.Of("acme")), "its project's org")
	require.Equal(t, 0, broken(ptr.Of("home")), "the default org, which it is not about")
}

// An item whose org read failed is undecided: it is never taken for one of no
// org, so the default org (which lists the rows with no org yet) does not get
// it, nor does any other named org; the unscoped list keeps it.
func TestInboxReads_AFailedOrgReadFailsClosed(t *testing.T) {
	repo := newInflightRepo(0)
	repo.GetProjectFunc = func(_ context.Context, id string) (*db.Project, error) {
		if id == "p-broken" {
			return nil, errors.New("database unavailable")
		}
		return &db.Project{ID: id, UserID: "user-1"}, nil // no org yet
	}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	ctx := authz.WithMembership(userCtx(), memberOf{"home": true, "acme": true})
	reads := svc.readableItem()
	failed := &reliantv1.InboxItem{ItemId: "failed", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN, ProjectId: "p-broken"}
	legacy := &reliantv1.InboxItem{ItemId: "legacy", Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN, ProjectId: "p-legacy"}
	for _, it := range []*reliantv1.InboxItem{failed, legacy} {
		reads.resolveOrg(ctx, "user-1", it)
	}
	read := &inboxReads{orgs: reads.orgs}
	ids := func(scope *core.OrgScope) []string {
		var out []string
		for _, it := range read.inScope([]*reliantv1.InboxItem{failed, legacy}, scope) {
			out = append(out, it.ItemId)
		}
		return out
	}
	require.Equal(t, []string{"failed", "legacy"}, ids(nil), "no org named")
	require.Equal(t, []string{"legacy"}, ids(&core.OrgScope{OrgID: "home", IncludeUnassigned: true}), "the default org: the legacy item, never the undecided one")
	require.Empty(t, ids(&core.OrgScope{OrgID: "acme"}), "another org")
}
