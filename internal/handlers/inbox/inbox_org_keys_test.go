// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// A rejected org key (org-provider-accounts §4.7) is listed to the org's owners
// and admins only: never to a member, and not on an unknown role or an outage.
// It is informational, one item per key per incident, and placed in its org.
func TestListInbox_RejectedOrgKeysReachOwnersAndAdmins(t *testing.T) {
	rejectedAt := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	key := func(id, org string) core.ProviderAccount {
		at := rejectedAt
		return core.ProviderAccount{ID: id, OwnerKind: core.AccountOwnerOrg, OrgID: org, Provider: "anthropic",
			Label: "Acme prod", KeyHint: "a1b2", NeedsReauthAt: &at, NeedsReauthReason: "the provider rejected the saved API key: 401"}
	}
	roles := map[string]string{"acme": orgs.RoleAdmin, "owned": orgs.RoleOwner, "beta": orgs.RoleMember}
	repo := newInflightRepo(0)
	repo.ListRejectedOrgKeysNearFunc = func(_ context.Context, userID string) ([]core.ProviderAccount, error) {
		require.Equal(t, "user-1", userID)
		return []core.ProviderAccount{key("k-acme", "acme"), key("k-owned", "owned"), key("k-beta", "beta"),
			key("k-down", "down"), key("k-unknown", "unknown")}, nil
	}
	resolver := &MockOrgResolver{
		ListScopeFunc: func(context.Context, string, string, orgs.Backfill) (*core.OrgScope, error) { return nil, nil },
		RoleFunc: func(_ context.Context, _, orgID string) (string, bool, error) {
			switch orgID {
			case "down":
				return "", false, errors.New("control plane unavailable")
			case "unknown":
				return "", false, nil
			}
			return roles[orgID], true, nil
		},
	}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Orgs: resolver}))
	resp, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)

	var got []*reliantv1.InboxItem
	for _, it := range resp.Msg.Items {
		if it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_ORG_PROVIDER_KEY_REJECTED {
			got = append(got, it)
		}
	}
	require.Len(t, got, 2, "an admin's and an owner's org only")
	ids := []string{got[0].GetOrgProviderKeyRejected().GetAccountId(), got[1].GetOrgProviderKeyRejected().GetAccountId()}
	assert.ElementsMatch(t, []string{"k-acme", "k-owned"}, ids)
	it := got[0]
	p := it.GetOrgProviderKeyRejected()
	assert.Equal(t, inboxOrgKeyRejectedPrefix+p.GetAccountId()+"@20261113T093000.000000", it.ItemId, "one item per key per incident")
	assert.Equal(t, rejectedAt.Format(time.RFC3339Nano), it.WaitingSince)
	assert.Equal(t, "Acme prod", p.GetLabel())
	assert.Equal(t, "a1b2", p.GetKeyHint())
	assert.Contains(t, p.GetReason(), "401")
	assert.False(t, inboxItemBlocks(it), "informational: nothing waits on it")
	assert.Zero(t, resp.Msg.BlockingCount)

	// Its org decides ListInbox's org scope.
	var reads inboxReads
	org, placed := reads.itemOrg(it)
	assert.Equal(t, placedInOrg, placed)
	require.NotNil(t, org)
	assert.Equal(t, p.GetOrgId(), *org)
}

// No orgs on the deployment: nothing is read.
func TestListInbox_RejectedOrgKeysNeedOrgs(t *testing.T) {
	repo := newInflightRepo(0)
	called := false
	repo.ListRejectedOrgKeysNearFunc = func(context.Context, string) ([]core.ProviderAccount, error) {
		called = true
		return nil, nil
	}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	assert.False(t, called)
}
