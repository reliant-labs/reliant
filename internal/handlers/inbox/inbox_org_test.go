// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
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

	require.Equal(t, sorted(approval("chat-acme"), approval("chat-home"), approval("chat-legacy"), approval("run-acme"), "recovery:gone"),
		itemIDs(nil), "no org named: every org's, as before")
	require.Equal(t, sorted(approval("chat-acme"), approval("run-acme")), itemIDs(ptr.Of("acme")), "acme")
	require.Equal(t, sorted(approval("chat-home"), approval("chat-legacy"), "recovery:gone"), itemIDs(ptr.Of("home")),
		"home, the default org, with the items of no org")

	_, err := newSvc().ListInbox(ctx, connect.NewRequest(&reliantv1.ListInboxRequest{OrgId: ptr.Of("globex")}))
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "an org the caller is not in")
}

// An item is judged by its chat, else its automation, else its project; a
// machine's storage item is about none of them and belongs to every org.
func TestInboxReads_ItemOrg(t *testing.T) {
	acme, home := "acme", "home"
	r := &inboxReads{orgs: map[string]*string{"chat:c": &acme, "trigger:t": &home, "project:p": &acme, "project:legacy": nil}}
	cases := []struct {
		it    *reliantv1.InboxItem
		org   *string
		about bool
	}{
		{&reliantv1.InboxItem{ChatId: "c", TriggerId: "t", ProjectId: "p"}, &acme, true},
		{&reliantv1.InboxItem{TriggerId: "t", ProjectId: "p"}, &home, true},
		{&reliantv1.InboxItem{ProjectId: "p"}, &acme, true},
		{&reliantv1.InboxItem{ProjectId: "legacy"}, nil, true},
		{&reliantv1.InboxItem{Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE}, nil, false},
	}
	for _, c := range cases {
		org, about := r.itemOrg(c.it)
		require.Equal(t, c.about, about, "%v", c.it)
		require.Equal(t, c.org, org, "%v", c.it)
	}
}
