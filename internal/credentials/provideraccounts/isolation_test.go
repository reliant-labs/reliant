// Copyright (c) 2025 Reliant Labs

package provideraccounts_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A credential of user A is never returned to user B, by any read or write that takes an account
// id, a user id or an owner: B gets NotFound (or nothing), and A's secret never appears.
func TestCredentialOfUserAIsNeverReturnedToUserB(t *testing.T) {
	t.Parallel()
	repo, _ := newEnv(t)
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("userA", "alice", "o", "SECRET-OF-A", "REFRESH-OF-A"))
	b := mustUpsert(t, repo, claudeUpsert("userB", "bob", "o", "SECRET-OF-B", "REFRESH-OF-B"))

	// B names A's account id.
	_, err := repo.GetProviderAccountWithSecrets(ctx, "userB", a.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.GetOwnedProviderAccountWithSecrets(ctx, core.UserAccountOwner("userB"), a.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.PatchProviderAccount(ctx, "userB", a.ID, core.AccountPatch{})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, "userB", a.ID), core.ErrProviderAccountNotFound)
	assert.Error(t, repo.ReorderProviderAccounts(ctx, "userB", "claude", []string{a.ID}))
	swapped, err := repo.CompareAndSwapAccountTokens(ctx, "userB", a.ID, "REFRESH-OF-A", core.AccountTokens{Secret: "HIJACK", Refresh: "HIJACK"})
	require.NoError(t, err)
	assert.False(t, swapped, "B's id cannot swap A's refresh token even knowing it")

	// B's lists hold only B's.
	meta, err := repo.ListProviderAccounts(ctx, "userB")
	require.NoError(t, err)
	require.Len(t, meta, 1)
	assert.Equal(t, b.ID, meta[0].ID)
	open, err := repo.ListProviderAccountsWithSecrets(ctx, "userB")
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "SECRET-OF-B", open[0].Secrets.Secret)

	// A is untouched.
	got, err := repo.GetProviderAccountWithSecrets(ctx, "userA", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "SECRET-OF-A", got.Secrets.Secret)
	assert.Equal(t, "REFRESH-OF-A", got.Secrets.Refresh)
}

// An empty user id reaches nothing: a list with no owner must not return every row.
func TestEmptyOwnerReadsNothing(t *testing.T) {
	t.Parallel()
	repo, _ := newEnv(t)
	ctx := context.Background()
	mustUpsert(t, repo, claudeUpsert("userA", "alice", "o", "SECRET-OF-A", "R"))
	mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))

	for _, owner := range []core.AccountOwner{{}, core.UserAccountOwner(""), core.OrgAccountOwner("")} {
		meta, err := repo.ListOwnedProviderAccounts(ctx, owner)
		require.NoError(t, err)
		assert.Empty(t, meta, "owner %+v", owner)
		open, err := repo.ListOwnedProviderAccountsWithSecrets(ctx, owner)
		require.NoError(t, err)
		assert.Empty(t, open, "owner %+v", owner)
		limits, err := repo.ListOwnedAccountLimits(ctx, owner)
		require.NoError(t, err)
		assert.Empty(t, limits, "owner %+v", owner)
	}
	none, err := repo.ListProviderAccountsWithSecrets(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, none)
	limits, err := repo.ListAccountLimits(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, limits)
}

// A credential of org A is never returned to org B, to a member of org A acting as a person, or to a
// person whose id is shaped like org A's owner key.
func TestCredentialOfOrgAIsNeverReturnedToOrgB(t *testing.T) {
	t.Parallel()
	repo, _ := newEnv(t)
	ctx := context.Background()
	orgA := mustUpsert(t, repo, orgKeyUpsert("org-a", "adminA", "acme", "SECRET-OF-ORG-A"))
	orgB := mustUpsert(t, repo, orgKeyUpsert("org-b", "adminB", "globex", "SECRET-OF-ORG-B"))
	mine := mustUpsert(t, repo, apiKeyUpsert("adminA", "mine", "SECRET-OF-ADMIN-A-PERSONAL"))

	bOwner, aOwner := core.OrgAccountOwner("org-b"), core.OrgAccountOwner("org-a")
	_, err := repo.GetOwnedProviderAccountWithSecrets(ctx, bOwner, orgA.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "org B names org A's account id")
	_, err = repo.PatchOwnedProviderAccount(ctx, bOwner, orgA.ID, core.AccountPatch{})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.DeleteOwnedProviderAccount(ctx, bOwner, orgA.ID), core.ErrProviderAccountNotFound)
	assert.Error(t, repo.ReorderOwnedProviderAccounts(ctx, bOwner, "anthropic", []string{orgA.ID}))
	assert.ErrorIs(t, repo.BenchAccount(ctx, bOwner, orgA.ID, time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC), "five_hour", false), core.ErrProviderAccountNotFound)

	listB, err := repo.ListOwnedProviderAccountsWithSecrets(ctx, bOwner)
	require.NoError(t, err)
	require.Len(t, listB, 1)
	assert.Equal(t, orgB.ID, listB[0].ID)
	assert.Equal(t, "SECRET-OF-ORG-B", listB[0].Secrets.Secret)

	// Org A's admin as a person, and a person named like org A's owner key, see only their own.
	for _, user := range []string{"adminA", "org:org-a", "org-a"} {
		personal, err := repo.ListProviderAccountsWithSecrets(ctx, user)
		require.NoError(t, err)
		for _, p := range personal {
			assert.NotEqual(t, orgA.ID, p.ID, "user %q must not be handed the org's account", user)
			assert.NotContains(t, p.Secrets.Secret, "SECRET-OF-ORG-A")
		}
		_, err = repo.GetProviderAccountWithSecrets(ctx, user, orgA.ID)
		assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "user %q", user)
		assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, user, orgA.ID), core.ErrProviderAccountNotFound, "user %q", user)
	}
	// ... and the org's owner never reaches the admin's personal account.
	_, err = repo.GetOwnedProviderAccountWithSecrets(ctx, aOwner, mine.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)

	got, err := repo.GetOwnedProviderAccountWithSecrets(ctx, aOwner, orgA.ID)
	require.NoError(t, err)
	assert.Equal(t, "SECRET-OF-ORG-A", got.Secrets.Secret)
}

// A member's grants are read for one org and one person only.
func TestListMemberAccountGrantsNeverReachesAnotherUsersOrOrgsGrants(t *testing.T) {
	t.Parallel()
	repo, raw := newEnv(t)
	ctx := context.Background()
	a := mustUpsert(t, repo, orgKeyUpsert("org-a", "adminA", "acme", "sk-ant-org-key-0001"))
	b := mustUpsert(t, repo, orgKeyUpsert("org-b", "adminB", "globex", "sk-ant-org-key-0002"))
	_, err := raw.ExecContext(ctx, `INSERT INTO provider_account_grants (org_id, account_id, user_id, granted_by_user_id) VALUES
		('org-a', $1, 'm1', 'adminA'), ('org-b', $2, 'm1', 'adminB'), ('org-a', $1, 'm2', 'adminA')`, a.ID, b.ID)
	require.NoError(t, err)

	got, err := repo.ListMemberAccountGrants(ctx, "org-a", "m1")
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Contains(t, got, a.ID)
	assert.NotContains(t, got, b.ID, "org B's grant is not org A's")
	none, err := repo.ListMemberAccountGrants(ctx, "org-a", "stranger")
	require.NoError(t, err)
	assert.Empty(t, none)
}
