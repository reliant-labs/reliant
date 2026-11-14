// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The administration of an org's provider accounts (control-plane
// docs/design/org-provider-accounts.md §3.3–§3.5, §6; §14 P4).

func orgKey(org, key string) core.OrgAPIKey {
	return core.OrgAPIKey{
		IdentityKey: core.APIKeyIdentityKey(core.OrgAccountOwner(org).Key(), "anthropic", key),
		KeyHint:     core.KeyHint(key), Secret: key,
	}
}

func orgKeyRow(org, creator, key string) core.AccountUpsert {
	k := orgKey(org, key)
	return core.AccountUpsert{
		Owner: core.OrgAccountOwner(org), OrgAccess: core.OrgAccessSelected, CreatedByUserID: creator,
		Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: k.IdentityKey,
		KeyHint: k.KeyHint, Secrets: core.AccountSecrets{Secret: key},
	}
}

func orgPriorities(t *testing.T, repo *Repo, org string) []string {
	t.Helper()
	all, err := repo.ListOwnedProviderAccounts(context.Background(), core.OrgAccountOwner(org))
	require.NoError(t, err)
	var ids []string
	for _, a := range all {
		require.Equal(t, len(ids), a.Priority, "priorities must be dense and ordered")
		ids = append(ids, a.ID)
	}
	return ids
}

// Rotation replaces the secret in place: the account keeps its id, and with it every thread
// assigned to it, its limit state, grants, label, order and pause; the needs-attention mark is
// cleared, and the new key (only) opens under the org's tenant.
func TestRotateOrgAPIKeyKeepsTheAccountAndItsAssignments(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	owner := core.OrgAccountOwner("org-1")

	first := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-first-key-0001"))
	acct := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-old-key-0002"))
	label, paused, reason := "Acme prod", true, "the provider rejected the key"
	_, err := repo.PatchOwnedProviderAccount(ctx, owner, acct.ID, core.AccountPatch{Label: &label, Paused: &paused, NeedsReauth: &reason})
	require.NoError(t, err)
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"dana"}, "ada"))
	grantsBefore, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	chat := createTestChat(t, repo)
	insertTestThreadForWorkflow(t, repo, "th-rot", chat, "wf-rot", ThreadStatusRunning)
	ok, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-rot", Provider: "anthropic", AccountID: acct.ID, Reason: "initial"}, "")
	require.NoError(t, err)
	require.True(t, ok)
	until := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) // a bench to carry over, not a clock
	require.NoError(t, repo.BenchAccount(ctx, owner, acct.ID, until, "minute", false))

	got, err := repo.RotateOrgAPIKey(ctx, "org-1", acct.ID, orgKey("org-1", "sk-ant-new-key-0003"))
	require.NoError(t, err)
	assert.Equal(t, acct.ID, got.ID, "same account")
	assert.Equal(t, orgKey("org-1", "sk-ant-new-key-0003").IdentityKey, got.IdentityKey, "a new fingerprint")
	assert.Equal(t, "0003", got.KeyHint)
	assert.Nil(t, got.NeedsReauthAt, "rotation clears needs-attention")
	assert.Equal(t, label, got.Label)
	assert.NotNil(t, got.PausedAt, "a pause is the admin's and stays")
	assert.Equal(t, 1, got.Priority)
	assert.Equal(t, core.OrgAccessSelected, got.OrgAccess)
	assert.Equal(t, []string{first.ID, acct.ID}, orgPriorities(t, repo, "org-1"))

	opened, err := repo.GetOwnedProviderAccountWithSecrets(ctx, owner, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-new-key-0003", opened.Secrets.Secret)

	a, err := repo.GetThreadAssignment(ctx, "th-rot", "anthropic")
	require.NoError(t, err)
	require.NotNil(t, a)
	assert.Equal(t, acct.ID, a.AccountID, "the thread stays on the rotated account")
	limits, err := repo.ListOwnedAccountLimits(ctx, owner)
	require.NoError(t, err)
	require.Contains(t, limits, acct.ID, "limit state carries over")
	grantsAfter, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, grantsBefore, grantsAfter, "grants survive")

	// A key the org already holds as another account is refused, and nothing changes.
	_, err = repo.RotateOrgAPIKey(ctx, "org-1", acct.ID, orgKey("org-1", "sk-ant-first-key-0001"))
	assert.ErrorIs(t, err, core.ErrOrgAPIKeyInUse)
	opened, err = repo.GetOwnedProviderAccountWithSecrets(ctx, owner, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-new-key-0003", opened.Secrets.Secret)

	// Another org, or a person, cannot rotate it.
	_, err = repo.RotateOrgAPIKey(ctx, "org-2", acct.ID, orgKey("org-2", "sk-ant-other-org-0004"))
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
}

// Grants replace as a set: every listed member is granted as of the call, one left out loses
// theirs; a delete takes every grant with the account.
func TestOrgAccountGrantsReplaceAsASetAndCascade(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)
	repo.accounts.SetClock(func() time.Time { return t0 })
	acct := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-org-key-0001"))
	other := mustUpsert(t, repo, orgKeyRow("org-2", "ben", "sk-ant-org-key-0002"))

	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"dana", "eve"}, "ada"))
	before, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, before, 2)
	for _, g := range before {
		assert.True(t, g.GrantedAt.Equal(t0), "%+v", g)
	}

	repo.accounts.SetClock(func() time.Time { return t1 })
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"eve", "fay"}, "ben"))
	after, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	byUser := map[string]core.OrgAccountGrant{}
	for _, g := range after {
		byUser[g.UserID] = g
		assert.Equal(t, "org-1", g.OrgID)
		assert.Equal(t, "ben", g.GrantedByUserID)
		assert.True(t, g.GrantedAt.Equal(t1), "every listed member is granted as of the call: %+v", g)
	}
	assert.ElementsMatch(t, []string{"eve", "fay"}, keys(byUser), "dana, left out, lost hers")

	// Another org's account is not found, and its grants are not this org's.
	assert.ErrorIs(t, repo.SetOrgAccountGrants(ctx, "org-1", other.ID, []string{"dana"}, "ada"), core.ErrProviderAccountNotFound)

	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, nil, "ada"))
	none, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	assert.Empty(t, none)

	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"dana"}, "ada"))
	require.NoError(t, repo.DeleteOwnedProviderAccount(ctx, core.OrgAccountOwner("org-1"), acct.ID))
	var n int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM provider_account_grants WHERE account_id=$1`, acct.ID).Scan(&n))
	assert.Zero(t, n, "grants cascade with the account")
}

// A grant counts only from the membership it was made in (granted_at >= member_since). A member
// who left keeps an inert row; when they rejoin and an admin grants them again, the grant is
// renewed to the new membership, never left at its stale time.
func TestRegrantAfterLeaveAndRejoinRenewsTheGrant(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	joined := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	repo.accounts.SetClock(func() time.Time { return joined.Add(time.Hour) })
	acct := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-org-key-0001"))
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"dana"}, "ada"))

	// dana leaves (her row stays, inert) and rejoins: her new membership starts at rejoined.
	rejoined := joined.Add(7 * 24 * time.Hour)
	stale, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.True(t, stale[0].GrantedAt.Before(rejoined), "the old grant predates the new membership")

	repo.accounts.SetClock(func() time.Time { return rejoined.Add(time.Minute) })
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-1", acct.ID, []string{"dana"}, "olga"))
	renewed, err := repo.ListOrgAccountGrants(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, renewed, 1)
	assert.False(t, renewed[0].GrantedAt.Before(rejoined), "the re-grant counts in the new membership: %+v", renewed[0])
	assert.Equal(t, "olga", renewed[0].GrantedByUserID)
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Access, order and delete act on the org's own rows only.
func TestOrgAccountAccessOrderAndDeleteAreScopedToTheOrg(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	owner := core.OrgAccountOwner("org-1")
	a := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-org-key-0001"))
	b := mustUpsert(t, repo, orgKeyRow("org-1", "ada", "sk-ant-org-key-0002"))
	mine := mustUpsert(t, repo, apiKeyUpsert("ada", "mine", "sk-ant-personal-0003"))

	got, err := repo.SetOrgAccountAccess(ctx, "org-1", a.ID, core.OrgAccessAdmins)
	require.NoError(t, err)
	assert.Equal(t, core.OrgAccessAdmins, got.OrgAccess)
	_, err = repo.SetOrgAccountAccess(ctx, "org-2", a.ID, core.OrgAccessMembers)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.SetOrgAccountAccess(ctx, "org-1", mine.ID, core.OrgAccessMembers)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "a person's account has no org access")
	_, err = repo.SetOrgAccountAccess(ctx, "org-1", a.ID, "everyone")
	assert.Error(t, err)

	require.NoError(t, repo.ReorderOwnedProviderAccounts(ctx, owner, "anthropic", []string{b.ID, a.ID}))
	assert.Equal(t, []string{b.ID, a.ID}, orgPriorities(t, repo, "org-1"))
	assert.ErrorIs(t, repo.ReorderOwnedProviderAccounts(ctx, owner, "anthropic", []string{b.ID, mine.ID}), core.ErrInvalidAccountOrder)

	assert.ErrorIs(t, repo.DeleteOwnedProviderAccount(ctx, core.OrgAccountOwner("org-2"), a.ID), core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.DeleteOwnedProviderAccount(ctx, core.UserAccountOwner("ada"), a.ID), core.ErrProviderAccountNotFound)
	require.NoError(t, repo.DeleteOwnedProviderAccount(ctx, owner, b.ID))
	assert.Equal(t, []string{a.ID}, orgPriorities(t, repo, "org-1"), "priorities re-densify")
}

// UNIQUE (provider, external_org_id): one workspace pays for one org. Binding it to the same org
// again is a no-op; to another org is refused without naming the holder.
func TestOrgProviderWorkspacePaysForOneOrg(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	w := core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-uuid", DisplayName: "Acme Inc.", BoundByUserID: "ada"}

	got, created, err := repo.BindOrgProviderWorkspace(ctx, w)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "Acme Inc.", got.DisplayName)
	assert.False(t, got.BoundAt.IsZero())

	again, created, err := repo.BindOrgProviderWorkspace(ctx, core.OrgProviderWorkspace{
		OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-uuid", DisplayName: "renamed", BoundByUserID: "eve",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, got.BoundByUserID, again.BoundByUserID, "the existing binding is returned unchanged")

	elsewhere := w
	elsewhere.OrgID, elsewhere.BoundByUserID = "org-2", "ben"
	_, _, err = repo.BindOrgProviderWorkspace(ctx, elsewhere)
	assert.ErrorIs(t, err, core.ErrProviderWorkspaceBoundElsewhere)

	// The same id at another provider is another workspace.
	codex := elsewhere
	codex.Provider = "codex"
	_, created, err = repo.BindOrgProviderWorkspace(ctx, codex)
	require.NoError(t, err)
	assert.True(t, created)

	one, err := repo.ListOrgProviderWorkspaces(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, "ws-uuid", one[0].ExternalOrgID)
}

// Unbinding clears the attributions that binding made, and only those: another workspace's seats,
// another org's, an approved seat and the managed key minted in the org stay. Releasing a seat
// clears that one account only, and never the managed key.
func TestUnbindAndReleaseClearOnlyTheirSeats(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	attribute := func(id, org, source string) {
		t.Helper()
		_, err := raw.ExecContext(ctx, `UPDATE provider_accounts SET attributed_org_id=$2, attribution_source=$3 WHERE id=$1`, id, org, source)
		require.NoError(t, err)
	}
	attribution := func(id string) string {
		t.Helper()
		var org *string
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT attributed_org_id FROM provider_accounts WHERE id=$1`, id).Scan(&org))
		if org == nil {
			return ""
		}
		return *org
	}
	seatA := mustUpsert(t, repo, claudeUpsert("dana", "dana", "ws-a", "t", "r"))
	seatA2 := mustUpsert(t, repo, claudeUpsert("eve", "eve", "ws-a", "t", "r"))
	seatB := mustUpsert(t, repo, claudeUpsert("fay", "fay", "ws-b", "t", "r"))
	approved := mustUpsert(t, repo, claudeUpsert("gus", "gus", "ws-a", "t", "r"))
	otherOrg := mustUpsert(t, repo, claudeUpsert("hal", "hal", "ws-a", "t", "r"))
	attribute(seatA.ID, "org-1", core.AttributionWorkspace)
	attribute(seatA2.ID, "org-1", core.AttributionWorkspace)
	attribute(seatB.ID, "org-1", core.AttributionWorkspace)
	attribute(approved.ID, "org-1", core.AttributionApproved)
	attribute(otherOrg.ID, "org-2", core.AttributionWorkspace)
	managed, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: "dana", Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey,
		Secrets: core.AccountSecrets{Secret: "rk-managed-0001"},
	})
	require.NoError(t, err)
	attribute(managed.ID, "org-1", core.AttributionManaged)

	seats, err := repo.ListOrgSeats(ctx, "org-1")
	require.NoError(t, err)
	var seatIDs []string
	for _, s := range seats {
		seatIDs = append(seatIDs, s.ID)
	}
	assert.ElementsMatch(t, []string{seatA.ID, seatA2.ID, seatB.ID, approved.ID}, seatIDs, "the managed key is not a seat")

	for _, ext := range []string{"ws-a", "ws-b"} {
		_, _, err := repo.BindOrgProviderWorkspace(ctx, core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: ext, DisplayName: ext, BoundByUserID: "ada"})
		require.NoError(t, err)
	}
	require.NoError(t, repo.UnbindOrgProviderWorkspace(ctx, "org-1", "claude", "ws-a"))
	assert.Empty(t, attribution(seatA.ID))
	assert.Empty(t, attribution(seatA2.ID))
	assert.Equal(t, "org-1", attribution(seatB.ID), "another workspace's seat stays")
	assert.Equal(t, "org-1", attribution(approved.ID), "an approved seat is not the binding's")
	assert.Equal(t, "org-2", attribution(otherOrg.ID), "another org's seat stays")
	assert.Equal(t, "org-1", attribution(managed.ID))
	assert.ErrorIs(t, repo.UnbindOrgProviderWorkspace(ctx, "org-1", "claude", "ws-a"), core.ErrProviderWorkspaceNotFound)
	assert.ErrorIs(t, repo.UnbindOrgProviderWorkspace(ctx, "org-2", "claude", "ws-b"), core.ErrProviderWorkspaceNotFound, "another org's binding")

	assert.ErrorIs(t, repo.ReleaseOrgSeat(ctx, "org-1", managed.ID, "ada"), core.ErrOrgSeatNotFound, "the managed key is never released")
	assert.ErrorIs(t, repo.ReleaseOrgSeat(ctx, "org-1", otherOrg.ID, "ada"), core.ErrOrgSeatNotFound, "another org's seat")
	require.NoError(t, repo.ReleaseOrgSeat(ctx, "org-1", approved.ID, "ada"))
	assert.Empty(t, attribution(approved.ID))
	assert.Equal(t, "org-1", attribution(seatB.ID))
	assert.ErrorIs(t, repo.ReleaseOrgSeat(ctx, "org-1", approved.ID, "ada"), core.ErrOrgSeatNotFound, "already released")
}
