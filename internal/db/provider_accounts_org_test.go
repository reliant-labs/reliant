// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Org-owned provider accounts (control-plane docs/design/org-provider-accounts.md §3.1, §3.5,
// §3.8). Nothing in the product writes one yet; the store can, and these pin what it promises.

func orgKeyUpsert(org, creator, label, key string) core.AccountUpsert {
	return core.AccountUpsert{
		Owner: core.OrgAccountOwner(org), OrgAccess: core.OrgAccessMembers, CreatedByUserID: creator,
		Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:" + label,
		KeyHint: key[len(key)-4:], Secrets: core.AccountSecrets{Secret: key},
	}
}

func apiKeyUpsert(user, label, key string) core.AccountUpsert {
	return core.AccountUpsert{
		UserID: user, Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:" + label,
		KeyHint: key[len(key)-4:], Secrets: core.AccountSecrets{Secret: key},
	}
}

func TestOrgAccountIsKeyedByItsOwnerAndRecordsItsCreator(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	mine := mustUpsert(t, repo, apiKeyUpsert("admin", "mine", "sk-ant-personal-0001"))
	org := mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))

	assert.Equal(t, core.OrgAccountOwner("org-1"), org.Owner())
	assert.Equal(t, "org:org-1", org.UserID, "the stored owner key")
	assert.Equal(t, core.OrgAccessMembers, org.OrgAccess)
	assert.Equal(t, "admin", org.CreatedByUserID)
	assert.Equal(t, 0, org.Priority, "an org has its own priority order, apart from its creator's")
	assert.Equal(t, 0, mine.Priority)
	assert.Equal(t, core.UserAccountOwner("admin"), mine.Owner())

	var kind, orgID string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT owner_kind, org_id FROM provider_accounts WHERE id=$1`, org.ID).Scan(&kind, &orgID))
	assert.Equal(t, "org", kind)
	assert.Equal(t, "org-1", orgID)

	got, err := repo.GetOwnedProviderAccountWithSecrets(ctx, core.OrgAccountOwner("org-1"), org.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-org-key-0002", got.Secrets.Secret)
	owned, err := repo.ListOwnedProviderAccounts(ctx, core.OrgAccountOwner("org-1"))
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.Equal(t, org.ID, owned[0].ID)
}

// An org's key is sealed under the org's vault tenant, and only that tenant opens it: never its
// creator's, never a person's tenant whose id is the owner key, never another org's.
func TestOrgAccountSealsUnderTheOrgTenantOnly(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	org := mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))

	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT secret_sealed FROM provider_accounts WHERE id=$1`, org.ID).Scan(&sealed))
	assert.NotContains(t, string(sealed), "sk-ant-org-key-0002")
	aad := ProviderAccountAAD(org.ID, accountColSecret)

	plain, err := repo.accounts.sealer.Open(ctx, vault.OrgTenant("org-1"), sealed, aad)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-org-key-0002", string(plain))
	for _, wrong := range []vault.Tenant{
		vault.UserTenant("admin"), vault.UserTenant("org:org-1"), vault.UserTenant("org-1"), vault.OrgTenant("org-2"),
	} {
		_, err := repo.accounts.sealer.Open(ctx, wrong, sealed, aad)
		assert.Error(t, err, "an org account's secret must not open under %+v", wrong)
	}

	// No store path opens it as anyone else's.
	for _, owner := range []core.AccountOwner{
		core.UserAccountOwner("admin"), core.UserAccountOwner("org:org-1"), core.UserAccountOwner("org-1"), core.OrgAccountOwner("org-2"),
	} {
		_, err := repo.GetOwnedProviderAccountWithSecrets(ctx, owner, org.ID)
		assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "owner %+v", owner)
	}
	_, err = repo.GetProviderAccountWithSecrets(ctx, "org:org-1", org.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "a person-scoped read by the owner key must not reach the org's row")

	// The ciphertext copied onto a person's row (its AAD binds the row, its tenant the org) does not open.
	mine := mustUpsert(t, repo, apiKeyUpsert("admin", "mine", "sk-ant-personal-0001"))
	_, err = raw.ExecContext(ctx, `UPDATE provider_accounts SET secret_sealed=$1 WHERE id=$2`, sealed, mine.ID)
	require.NoError(t, err)
	_, err = repo.GetProviderAccountWithSecrets(ctx, "admin", mine.ID)
	assert.Error(t, err, "an org's ciphertext moved onto a person's row must not open")

	var orgKeys, adminKeys int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM vault_keys WHERE tenant_kind='org' AND tenant_id='org-1'),
		(SELECT count(*) FROM vault_keys WHERE tenant_kind='user' AND tenant_id='admin')`).Scan(&orgKeys, &adminKeys))
	assert.Equal(t, 1, orgKeys)
	assert.Equal(t, 1, adminKeys, "only the admin's own key made their tenant")
}

// Every person-scoped read, write and list is blind to org rows, including when handed the owner
// key as a user id.
func TestPersonalAccountMethodsNeverReachAnOrgAccount(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	mine := mustUpsert(t, repo, apiKeyUpsert("admin", "mine", "sk-ant-personal-0001"))
	org := mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))

	for _, user := range []string{"admin", "org:org-1", "org-1"} {
		list, err := repo.ListProviderAccounts(ctx, user)
		require.NoError(t, err)
		withSecrets, err := repo.ListProviderAccountsWithSecrets(ctx, user)
		require.NoError(t, err)
		for _, a := range list {
			assert.NotEqual(t, org.ID, a.ID, "ListProviderAccounts(%q) returned the org's key", user)
		}
		for _, a := range withSecrets {
			assert.NotEqual(t, org.ID, a.ID, "ListProviderAccountsWithSecrets(%q) returned the org's key", user)
		}
	}
	list, err := repo.ListProviderAccounts(ctx, "admin")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, mine.ID, list[0].ID)

	paused := true
	_, err = repo.PatchProviderAccount(ctx, "org:org-1", org.ID, core.AccountPatch{Paused: &paused})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.PatchProviderAccount(ctx, "admin", org.ID, core.AccountPatch{Paused: &paused})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "the creator does not own the org's key")
	assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, "org:org-1", org.ID), core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, "admin", org.ID), core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.ReorderProviderAccounts(ctx, "org:org-1", "anthropic", []string{org.ID}), core.ErrInvalidAccountOrder)
	ok, err := repo.CompareAndSwapAccountTokens(ctx, "org:org-1", org.ID, "", core.AccountTokens{Secret: "x"})
	require.NoError(t, err)
	assert.False(t, ok)
	limits, err := repo.ListAccountLimits(ctx, "org:org-1")
	require.NoError(t, err)
	assert.Empty(t, limits)
	ids, err := repo.ListUserIDsWithProviderAccount(ctx, "anthropic")
	require.NoError(t, err)
	assert.Equal(t, []string{"admin"}, ids, "an org is not a person holding an account")

	still, err := repo.GetOwnedProviderAccountWithSecrets(ctx, core.OrgAccountOwner("org-1"), org.ID)
	require.NoError(t, err)
	assert.Nil(t, still.PausedAt)
	assert.Equal(t, "sk-ant-org-key-0002", still.Secrets.Secret)
}

// Writes about an org's account go to the org's row: a rejected key is marked on it and a limit
// benches it, for every member at once.
func TestOwnerAwareWritesReachTheOrgsAccount(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	owner := core.OrgAccountOwner("org-1")
	org := mustUpsert(t, repo, orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002"))

	reason := "the provider rejected the saved API key"
	got, err := repo.PatchOwnedProviderAccount(ctx, owner, org.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	require.NotNil(t, got.NeedsReauthAt)
	assert.Equal(t, reason, got.NeedsReauthReason)
	assert.Equal(t, owner, got.Owner())
	_, err = repo.PatchOwnedProviderAccount(ctx, core.OrgAccountOwner("org-2"), org.ID, core.AccountPatch{NeedsReauth: &reason})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound, "another org cannot mark it")

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.BenchAccount(ctx, owner, org.ID, until, "", false))
	assert.ErrorIs(t, repo.BenchAccount(ctx, core.UserAccountOwner("member"), org.ID, until, "", false), core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.BenchAccount(ctx, core.UserAccountOwner("org:org-1"), org.ID, until, "", false), core.ErrProviderAccountNotFound)
	l, err := repo.GetAccountLimits(ctx, org.ID)
	require.NoError(t, err)
	require.NotNil(t, l)
	assert.Equal(t, "org:org-1", l.UserID, "an org key's limits row is the org's, shared by its members")
	require.NotNil(t, l.LimitedUntil)
	assert.True(t, l.LimitedUntil.Equal(until))

	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{
		AccountID: org.ID, UserID: owner.Key(), Status: "allowed", ObservedAt: time.Now().Add(2 * time.Hour),
	}))
	l, err = repo.GetAccountLimits(ctx, org.ID)
	require.NoError(t, err)
	assert.Nil(t, l.LimitedUntil, "a newer allowed snapshot keyed by the owner lifts the bench")
}

func TestOrgAccountUpsertValidation(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	cases := map[string]func(*core.AccountUpsert){
		"sign-in":        func(u *core.AccountUpsert) { u.AuthKind = core.AuthKindOAuth },
		"no access":      func(u *core.AccountUpsert) { u.OrgAccess = "" },
		"unknown access": func(u *core.AccountUpsert) { u.OrgAccess = "everyone" },
		"no creator":     func(u *core.AccountUpsert) { u.CreatedByUserID = "" },
		"empty org":      func(u *core.AccountUpsert) { u.Owner = core.OrgAccountOwner("") },
		"owner-key user": func(u *core.AccountUpsert) {
			u.Owner, u.OrgAccess, u.CreatedByUserID = core.UserAccountOwner("org:org-1"), "", ""
		},
		"person's access": func(u *core.AccountUpsert) { u.Owner = core.UserAccountOwner("admin") },
	}
	for name, mut := range cases {
		u := orgKeyUpsert("org-1", "admin", "acme", "sk-ant-org-key-0002")
		mut(&u)
		_, _, err := repo.UpsertProviderAccount(ctx, u)
		assert.Error(t, err, name)
	}
	_, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: "org:org-1", Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:x",
		Secrets: core.AccountSecrets{Secret: "sk-x-0000"},
	})
	assert.Error(t, err, "a person's id never looks like an owner key")
}

// The CHECKs make the shape exact, whatever writes the row; a row the previous release would
// write (no owner columns) passes every one of them.
func TestProviderAccountOwnerChecks(t *testing.T) {
	t.Parallel()
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	insert := func(id, cols, vals string) error {
		_, err := raw.ExecContext(ctx, `INSERT INTO provider_accounts (id, provider, identity_key, priority, secret_sealed, `+cols+`)
			VALUES ('`+id+`', 'anthropic', 'key:`+id+`', 0, '\x01', `+vals+`)`)
		return err
	}

	require.NoError(t, insert("old-shape", "user_id, auth_kind", "'u1', 'api_key'"), "the previous release's insert")
	_, err := raw.ExecContext(ctx, `INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed)
		VALUES ('old-managed', 'u2', 'reliant', 'managed', 'managed', 0, '\x01')`)
	require.NoError(t, err, "the previous release's managed row")
	require.NoError(t, insert("org-ok", "user_id, owner_kind, org_id, org_access, auth_kind", "'org:o1', 'org', 'o1', 'members', 'api_key'"))
	require.NoError(t, insert("seat-ok", "user_id, auth_kind, attributed_org_id, attribution_source", "'u3', 'oauth', 'o1', 'workspace'"))

	refused := map[string][2]string{
		"unknown kind":          {"user_id, owner_kind, auth_kind", "'u4', 'team', 'api_key'"},
		"org without org_id":    {"user_id, owner_kind, org_access, auth_kind", "'org:o1', 'org', 'members', 'api_key'"},
		"org key mismatch":      {"user_id, owner_kind, org_id, org_access, auth_kind", "'org:o2', 'org', 'o1', 'members', 'api_key'"},
		"org key is a person":   {"user_id, owner_kind, org_id, org_access, auth_kind", "'u5', 'org', 'o1', 'members', 'api_key'"},
		"person with owner key": {"user_id, auth_kind", "'org:o1', 'api_key'"},
		"person with org_id":    {"user_id, org_id, auth_kind", "'u6', 'o1', 'api_key'"},
		"org sign-in":           {"user_id, owner_kind, org_id, org_access, auth_kind", "'org:o1', 'org', 'o1', 'members', 'oauth'"},
		"org without access":    {"user_id, owner_kind, org_id, auth_kind", "'org:o1', 'org', 'o1', 'api_key'"},
		"person with access":    {"user_id, org_access, auth_kind", "'u7', 'members', 'api_key'"},
		"unknown access":        {"user_id, owner_kind, org_id, org_access, auth_kind", "'org:o1', 'org', 'o1', 'everyone', 'api_key'"},
		"attribution no source": {"user_id, auth_kind, attributed_org_id", "'u8', 'oauth', 'o1'"},
		"source no attribution": {"user_id, auth_kind, attribution_source", "'u8', 'oauth', 'workspace'"},
		"unknown source":        {"user_id, auth_kind, attributed_org_id, attribution_source", "'u8', 'oauth', 'o1', 'declared'"},
		"org row attributed":    {"user_id, owner_kind, org_id, org_access, auth_kind, attributed_org_id, attribution_source", "'org:o1', 'org', 'o1', 'members', 'api_key', 'o2', 'approved'"},
	}
	for name, cv := range refused {
		assert.Error(t, insert("x-"+name, cv[0], cv[1]), name)
	}

	// The widened managed CHECK: 'managed' and 'managed:<org>', nothing else.
	_, err = raw.ExecContext(ctx, `INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed)
		VALUES ('m-org', 'u9', 'reliant', 'managed', 'managed:o1', 0, '\x01')`)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed)
		VALUES ('m-bad', 'u9', 'reliant', 'managed', 'key:other', 1, '\x01')`)
	assert.Error(t, err)
}

func TestAccountOwnerKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "u1", core.UserAccountOwner("u1").Key())
	assert.Equal(t, "org:o1", core.OrgAccountOwner("o1").Key())
	for _, bad := range []core.AccountOwner{
		core.UserAccountOwner(""), core.UserAccountOwner("org:o1"), core.OrgAccountOwner(""), {Kind: "team", ID: "x"}, {},
	} {
		assert.False(t, bad.Valid(), "%+v", bad)
		assert.Equal(t, "", bad.Key(), "an invalid owner's key matches no row: %+v", bad)
	}
	acct := core.ProviderAccount{UserID: "org:o1", OwnerKind: core.AccountOwnerOrg, OrgID: "o1"}
	assert.Equal(t, core.OrgAccountOwner("o1"), acct.Owner())
	assert.Equal(t, core.UserAccountOwner("u1"), core.ProviderAccount{UserID: "u1"}.Owner(), "a row predating owner_kind is its user's")
}
