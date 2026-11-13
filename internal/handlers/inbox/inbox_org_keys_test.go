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
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
)

var orgKeyRejectedAt = time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)

func rejectedOrgKey(id, org string) core.ProviderAccount {
	at := orgKeyRejectedAt
	return core.ProviderAccount{ID: id, OwnerKind: core.AccountOwnerOrg, OrgID: org, Provider: "anthropic",
		Label: "Acme prod", KeyHint: "a1b2", NeedsReauthAt: &at, NeedsReauthReason: "the provider rejected the saved API key: 401"}
}

// orgKeyEnv is an inbox whose caller's live orgs come from lister, over a store holding keys.
func orgKeyEnv(t *testing.T, lister *MockOrgLister, keys ...core.ProviderAccount) (*Service, *inflightRepo) {
	t.Helper()
	repo := newInflightRepo(0)
	repo.HasRejectedOrgKeysFunc = func(context.Context) (bool, error) { return len(keys) > 0, nil }
	repo.ListRejectedOrgKeysInFunc = func(_ context.Context, orgIDs []string) ([]core.ProviderAccount, error) {
		in := map[string]bool{}
		for _, id := range orgIDs {
			in[id] = true
		}
		var out []core.ProviderAccount
		for _, k := range keys {
			if in[k.OrgID] {
				out = append(out, k)
			}
		}
		return out, nil
	}
	deps := Deps{Repo: repo}
	if lister != nil {
		deps.MyOrgs = lister
	}
	return NewTestInbox(t, WithInboxDeps(deps)), repo
}

func listOrgKeys(t *testing.T, svc *Service, jwt string) ([]*reliantv1.InboxItem, *reliantv1.ListInboxResponse) {
	t.Helper()
	req := connect.NewRequest(&reliantv1.ListInboxRequest{})
	if jwt != "" {
		req.Header().Set("Authorization", "Bearer "+jwt)
	}
	resp, err := svc.ListInbox(userCtx(), req)
	require.NoError(t, err)
	var got []*reliantv1.InboxItem
	for _, it := range resp.Msg.Items {
		if it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_ORG_PROVIDER_KEY_REJECTED {
			got = append(got, it)
		}
	}
	return got, resp.Msg
}

// A rejected org key (org-provider-accounts §4.7) reaches the org's owners and
// admins, found from control-plane's live memberships, not from anything they
// have locally: an admin who has never opened a project of the org still sees
// it. A member does not. It is informational, one item per key per incident,
// and placed in its org.
func TestListInbox_RejectedOrgKeysReachOwnersAndAdmins(t *testing.T) {
	lister := &MockOrgLister{ListMyOrganizationsFunc: func(_ context.Context, jwt string) ([]controlplane.MyOrganization, error) {
		require.Equal(t, "session-jwt", jwt, "asked as the caller")
		return []controlplane.MyOrganization{{ID: "acme", Role: "admin"}, {ID: "owned", Role: "owner"}, {ID: "beta", Role: "member"}}, nil
	}}
	svc, _ := orgKeyEnv(t, lister, rejectedOrgKey("k-acme", "acme"), rejectedOrgKey("k-owned", "owned"),
		rejectedOrgKey("k-beta", "beta"), rejectedOrgKey("k-else", "elsewhere"))
	got, resp := listOrgKeys(t, svc, "session-jwt")
	require.Len(t, got, 2, "the orgs the caller administers, footprint or not")
	ids := []string{got[0].GetOrgProviderKeyRejected().GetAccountId(), got[1].GetOrgProviderKeyRejected().GetAccountId()}
	assert.ElementsMatch(t, []string{"k-acme", "k-owned"}, ids)

	it := got[0]
	p := it.GetOrgProviderKeyRejected()
	assert.Equal(t, inboxOrgKeyRejectedPrefix+p.GetAccountId()+"@20261113T093000.000000", it.ItemId, "one item per key per incident")
	assert.Equal(t, orgKeyRejectedAt.Format(time.RFC3339Nano), it.WaitingSince)
	assert.Equal(t, "Acme prod", p.GetLabel())
	assert.Equal(t, "a1b2", p.GetKeyHint())
	assert.Contains(t, p.GetReason(), "401")
	assert.False(t, inboxItemBlocks(it), "informational: nothing waits on it")
	assert.Zero(t, resp.BlockingCount)

	var reads inboxReads
	org, placed := reads.itemOrg(it)
	assert.Equal(t, placedInOrg, placed)
	require.NotNil(t, org)
	assert.Equal(t, p.GetOrgId(), *org)

	// The live answer is reused for a short while: the badge polls.
	_, _ = listOrgKeys(t, svc, "session-jwt")
	assert.Equal(t, 1, lister.CallCount("ListMyOrganizations"))
}

// Fail closed: with control-plane down, with no session to ask as (a machine
// credential, which has no orgs to list), or self-hosted, nothing is shown and
// nothing local is used instead. With no rejected key anywhere, nobody is asked.
func TestListInbox_RejectedOrgKeysFailClosed(t *testing.T) {
	key := rejectedOrgKey("k-acme", "acme")
	down := &MockOrgLister{ListMyOrganizationsFunc: func(context.Context, string) ([]controlplane.MyOrganization, error) {
		return nil, errors.New("control plane unavailable")
	}}
	svc, _ := orgKeyEnv(t, down, key)
	got, _ := listOrgKeys(t, svc, "session-jwt")
	assert.Empty(t, got, "an outage shows nothing")

	admin := &MockOrgLister{ListMyOrganizationsFunc: func(context.Context, string) ([]controlplane.MyOrganization, error) {
		return []controlplane.MyOrganization{{ID: "acme", Role: "admin"}}, nil
	}}
	svc, _ = orgKeyEnv(t, admin, key)
	got, _ = listOrgKeys(t, svc, "")
	assert.Empty(t, got, "no session to ask as")
	assert.Zero(t, admin.CallCount("ListMyOrganizations"))

	svc, _ = orgKeyEnv(t, nil, key)
	got, _ = listOrgKeys(t, svc, "session-jwt")
	assert.Empty(t, got, "self-hosted: no control plane")

	idle := &MockOrgLister{ListMyOrganizationsFunc: admin.ListMyOrganizationsFunc}
	svc, _ = orgKeyEnv(t, idle)
	got, _ = listOrgKeys(t, svc, "session-jwt")
	assert.Empty(t, got)
	assert.Zero(t, idle.CallCount("ListMyOrganizations"), "no rejected key anywhere: nobody is asked")
}
