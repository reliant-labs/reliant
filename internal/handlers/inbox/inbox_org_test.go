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
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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
			now := time.Now()
			return []*core.InboxPending{
				{Kind: core.InboxPendingApproval, ItemKey: "chat-acme", ChatID: "c-acme", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "chat-home", ChatID: "c-home", ProjectID: "p-home", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "run-acme", ProjectID: "p-acme", WaitingSince: now},
				{Kind: core.InboxPendingApproval, ItemKey: "run-home", ProjectID: "p-home", WaitingSince: now},
			}, nil
		}
		repo.ListWorktreeRecoveriesFunc = func(context.Context, string, string) ([]*core.WorktreeRecovery, error) {
			now := time.Now()
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
