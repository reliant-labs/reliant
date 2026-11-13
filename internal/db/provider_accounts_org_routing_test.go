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

// The reads the account loader makes for a run of an org
// (org-provider-accounts §4.1): the org's accounts with their secrets, their
// limit state under the org's owner key, and the member's grants.
func TestOrgAccountReadsForTheLoader(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	mine := mustUpsert(t, repo, apiKeyUpsert("member", "mine", "sk-ant-personal-0001"))
	acme := mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))
	mustUpsert(t, repo, orgKeyUpsert("org-2", "admin", "globex", "sk-ant-org-key-0003"))

	owned, err := repo.ListOwnedProviderAccountsWithSecrets(ctx, core.OrgAccountOwner("org-1"))
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.Equal(t, acme.ID, owned[0].ID)
	assert.Equal(t, "sk-ant-org-key-0002", owned[0].Secrets.Secret)
	none, err := repo.ListOwnedProviderAccountsWithSecrets(ctx, core.UserAccountOwner("org:org-1"))
	require.NoError(t, err)
	assert.Empty(t, none, "an owner key handed over as a person reads nothing")

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.BenchAccount(ctx, core.OrgAccountOwner("org-1"), acme.ID, until, "five_hour", false))
	limits, err := repo.ListOwnedAccountLimits(ctx, core.OrgAccountOwner("org-1"))
	require.NoError(t, err)
	require.Contains(t, limits, acme.ID)
	require.NotNil(t, limits[acme.ID].LimitedUntil)
	personal, err := repo.ListAccountLimits(ctx, "member")
	require.NoError(t, err)
	assert.NotContains(t, personal, acme.ID, "an org key's limits are the org's, not the member's")

	granted := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	_, err = raw.ExecContext(ctx, `INSERT INTO provider_account_grants (account_id, org_id, user_id, granted_by_user_id, granted_at)
		VALUES ($1, 'org-1', 'member', 'admin', $2)`, acme.ID, granted)
	require.NoError(t, err)
	grants, err := repo.ListMemberAccountGrants(ctx, "org-1", "member")
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.True(t, granted.Equal(grants[acme.ID]))
	for _, q := range [][2]string{{"org-2", "member"}, {"org-1", "someone-else"}, {"", "member"}} {
		got, err := repo.ListMemberAccountGrants(ctx, q[0], q[1])
		require.NoError(t, err)
		assert.Empty(t, got, "%v", q)
	}
	_ = mine
}
