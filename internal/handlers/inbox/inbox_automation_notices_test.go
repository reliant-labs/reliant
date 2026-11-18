// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
)

var noOrgAccountPausedAt = time.Date(2026, 11, 13, 22, 0, 0, 0, time.UTC)

func pausedForNoOrgAccount(id, owner, org string) *core.Trigger {
	at := noOrgAccountPausedAt
	return &core.Trigger{ID: id, UserID: owner, OrgID: &org, Name: "Nightly " + id, ProjectID: "p-" + id,
		Enabled: false, PausedReason: core.TriggerPausedNoOrgAccount, PausedAt: &at}
}

// memberCounts is the control plane's live member count of each org.
type memberCounts struct {
	n     map[string]int
	err   error
	calls atomic.Int32
}

func (m *memberCounts) LiveMembersFresh(_ context.Context, _, orgID string) (int, bool, error) {
	m.calls.Add(1)
	if m.err != nil {
		return 0, false, m.err
	}
	n, ok := m.n[orgID]
	return n, ok, nil
}

func listKinds(t *testing.T, svc *Service, ctx context.Context, jwt string, kind reliantv1.InboxItemKind) []*reliantv1.InboxItem {
	t.Helper()
	req := connect.NewRequest(&reliantv1.ListInboxRequest{})
	if jwt != "" {
		req.Header().Set("Authorization", "Bearer "+jwt)
	}
	resp, err := svc.ListInbox(ctx, req)
	require.NoError(t, err)
	var got []*reliantv1.InboxItem
	for _, it := range resp.Msg.Items {
		if it.Kind == kind {
			got = append(got, it)
		}
	}
	return got
}

// An automation paused for want of an org account (§11.4) reaches its owner,
// and the owners and admins of its org (control-plane's live answer, as for a
// rejected key): one item per pause, naming the owner for an admin, carrying
// what the pausing firing recorded, and placed in its org.
func TestListInbox_NoOrgAccountPauseReachesOwnerAndAdmins(t *testing.T) {
	repo := newInflightRepo(0)
	repo.HasNoOrgAccountPausesFunc = func(context.Context) (bool, error) { return true, nil }
	var asked []string
	repo.ListNoOrgAccountPausesFunc = func(_ context.Context, userID string, orgIDs []string) ([]*core.Trigger, error) {
		asked = orgIDs
		return []*core.Trigger{pausedForNoOrgAccount("t-mine", userID, "acme"), pausedForNoOrgAccount("t-theirs", "u-mia", "acme")}, nil
	}
	repo.GetLatestTriggerEventFunc = func(_ context.Context, id string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
		require.Equal(t, core.TriggerEventFailed, *outcome)
		return &core.TriggerEvent{OutcomeDetail: "none of them can run claude-opus-4-6 (" + id + ")"}, nil
	}
	lister := &MockOrgLister{ListMyOrganizationsFunc: func(context.Context, string) ([]controlplane.MyOrganization, error) {
		return []controlplane.MyOrganization{{ID: "acme", Role: "admin"}, {ID: "beta", Role: "member"}}, nil
	}}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, MyOrgs: lister}))

	got := listKinds(t, svc, sessionCtx(), "session-jwt", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT)
	assert.Equal(t, []string{"acme"}, asked, "the orgs the caller administers")
	require.Len(t, got, 2)
	byTrigger := map[string]*reliantv1.InboxItem{got[0].TriggerId: got[0], got[1].TriggerId: got[1]}
	mine, theirs := byTrigger["t-mine"], byTrigger["t-theirs"]
	require.NotNil(t, mine)
	require.NotNil(t, theirs)
	assert.Equal(t, inboxNoOrgAccountPrefix+"t-mine@20261113T220000.000000", mine.ItemId, "one item per pause")
	assert.Equal(t, noOrgAccountPausedAt.Format(time.RFC3339Nano), mine.WaitingSince)
	assert.Equal(t, "Nightly t-mine", mine.TriggerName)
	assert.Empty(t, mine.GetAutomationNoOrgAccount().GetOwnerUserId(), "the owner's own")
	assert.Equal(t, "u-mia", theirs.GetAutomationNoOrgAccount().GetOwnerUserId(), "an admin is told whose it is")
	assert.Contains(t, theirs.GetAutomationNoOrgAccount().GetDetail(), "claude-opus-4-6 (t-theirs)")
	assert.False(t, inboxItemBlocks(mine), "informational")
	var reads inboxReads
	org, placed := reads.itemOrg(theirs)
	assert.Equal(t, placedInOrg, placed)
	assert.Equal(t, "acme", *org)
}

// Without a session to ask as (an access token), or with the control plane
// down, only the caller's own paused automations are listed: never another's.
func TestListInbox_NoOrgAccountPauseOwnerOnlyWithoutAdminOrgs(t *testing.T) {
	for name, deps := range map[string]func(*inflightRepo) (Deps, context.Context, string){
		"an access token": func(r *inflightRepo) (Deps, context.Context, string) {
			return Deps{Repo: r, MyOrgs: &MockOrgLister{}}, auth.WithCredential(userCtx(), auth.CredentialAccessToken), "rlat_agent-token"
		},
		"control plane down": func(r *inflightRepo) (Deps, context.Context, string) {
			down := &MockOrgLister{ListMyOrganizationsFunc: func(context.Context, string) ([]controlplane.MyOrganization, error) {
				return nil, errors.New("unavailable")
			}}
			return Deps{Repo: r, MyOrgs: down}, sessionCtx(), "session-jwt"
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newInflightRepo(0)
			repo.HasNoOrgAccountPausesFunc = func(context.Context) (bool, error) { return true, nil }
			var asked []string
			repo.ListNoOrgAccountPausesFunc = func(_ context.Context, userID string, orgIDs []string) ([]*core.Trigger, error) {
				asked = orgIDs
				return []*core.Trigger{pausedForNoOrgAccount("t-mine", userID, "acme")}, nil
			}
			d, ctx, jwt := deps(repo)
			member := authz.WithMembership(ctx, memberOf{"home": true, "acme": true})
			got := listKinds(t, NewTestInbox(t, WithInboxDeps(d)), member, jwt, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT)
			assert.Empty(t, asked, "no admin orgs")
			assert.Len(t, got, 1, "the owner's own still")

			// An owner who has left the org no longer sees it (authz decides
			// each row the store scoped to them).
			left := authz.WithMembership(ctx, memberOf{"home": true})
			assert.Empty(t, listKinds(t, NewTestInbox(t, WithInboxDeps(d)), left, jwt, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT),
				"an org the owner left")
		})
	}
}

// The caller's automations still on their own accounts are listed per team
// org (§11.3): the release notice, and again when a solo org gains a member.
// A solo org, an org whose count is unknown, or a failed read lists nothing;
// the count is reused for a while; the key changes only when the set does.
func TestListInbox_AutomationsOnPersonalAccounts(t *testing.T) {
	repo := newInflightRepo(0)
	acme, solo := "acme", "solo"
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	set := []*core.Trigger{
		{ID: "t-1", UserID: "user-1", OrgID: &acme, CreatedAt: created},
		{ID: "t-2", UserID: "user-1", OrgID: &acme, CreatedAt: created.Add(time.Hour)},
		{ID: "t-3", UserID: "user-1", OrgID: &solo, CreatedAt: created},
	}
	repo.ListPersonallyFundedTriggersFunc = func(context.Context, string) ([]*core.Trigger, error) { return set, nil }
	counts := &memberCounts{n: map[string]int{"acme": 2, "solo": 1}}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Members: counts}))
	ctx := authz.WithMembership(sessionCtx(), memberOf{"home": true, "acme": true, "solo": true})

	got := listKinds(t, svc, ctx, "", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS)
	require.Len(t, got, 1, "the team org only")
	p := got[0].GetAutomationsOnPersonalAccounts()
	assert.Equal(t, "acme", p.GetOrgId())
	assert.EqualValues(t, 2, p.GetCount())
	assert.Equal(t, []string{"t-1", "t-2"}, p.GetTriggerIds())
	assert.Equal(t, created.Add(time.Hour).Format(time.RFC3339Nano), got[0].WaitingSince)
	key := got[0].ItemId
	assert.Contains(t, key, inboxOnPersonalAccountsPrefix+"acme@")
	var reads inboxReads
	org, placed := reads.itemOrg(got[0])
	assert.Equal(t, placedInOrg, placed)
	assert.Equal(t, "acme", *org)

	calls := counts.calls.Load()
	again := listKinds(t, svc, ctx, "", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS)
	assert.Equal(t, key, again[0].ItemId, "the same set is the same item")
	assert.Equal(t, calls, counts.calls.Load(), "the count is reused")

	set = append(set, &core.Trigger{ID: "t-4", UserID: "user-1", OrgID: &acme, CreatedAt: created})
	more := listKinds(t, NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Members: counts})), ctx, "",
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS)
	assert.NotEqual(t, key, more[0].ItemId, "a set that gains one is a new item, past a dismissal")

	for name, c := range map[string]*memberCounts{
		"an unknown count": {n: map[string]int{}},
		"a failed read":    {err: errors.New("down")},
	} {
		got := listKinds(t, NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Members: c})), ctx, "",
			reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS)
		assert.Empty(t, got, name)
	}
	assert.Empty(t, listKinds(t, NewTestInbox(t, WithInboxDeps(Deps{Repo: repo})), ctx, "",
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS), "no orgs on this deployment")

	// An org the caller has left: its automations are not theirs to see.
	left := authz.WithMembership(sessionCtx(), memberOf{"home": true, "solo": true})
	assert.Empty(t, listKinds(t, NewTestInbox(t, WithInboxDeps(Deps{Repo: repo, Members: counts})), left, "",
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS), "an org the caller left")
}
