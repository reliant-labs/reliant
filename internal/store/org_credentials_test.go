// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The lifecycle of an org's credentials (control-plane
// docs/design/org-provider-accounts.md §3.3, §6): the stale-grant sweep's
// reads and delete, and the gone-org purge.

// credT0 is the fixed time every fixture here is stamped with.
var credT0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

type credFixture struct {
	t   *testing.T
	env *storetest.Env
}

func newCredFixture(t *testing.T) *credFixture {
	return &credFixture{t: t, env: storetest.New(t)}
}

func (f *credFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.env.SQL.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err, query)
}

func (f *credFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.env.SQL.QueryRowContext(context.Background(), query, args...).Scan(&n), query)
	return n
}

// orgKey inserts an org-owned API key of org.
func (f *credFixture) orgKey(id, org string) {
	f.t.Helper()
	f.exec(`INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed,
		owner_kind, org_id, org_access, created_by_user_id)
		VALUES ($1, 'org:' || $2, 'anthropic', 'api_key', 'key:' || $1,
		(SELECT count(*) FROM provider_accounts WHERE user_id = 'org:' || $2), '\x01', 'org', $2, 'selected', 'admin')`, id, org)
}

// personal inserts a person's API key, attributed to attributedOrg with
// source when attributedOrg is not "".
func (f *credFixture) personal(id, user, attributedOrg, source string) {
	f.t.Helper()
	var org, src any
	if attributedOrg != "" {
		org, src = attributedOrg, source
	}
	f.exec(`INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed,
		attributed_org_id, attribution_source)
		VALUES ($1, $2, 'claude', 'api_key', 'key:' || $1, (SELECT count(*) FROM provider_accounts WHERE user_id = $2), '\x01', $3, $4)`,
		id, user, org, src)
}

func (f *credFixture) grant(accountID, org, user string, at time.Time) {
	f.t.Helper()
	f.exec(`INSERT INTO provider_account_grants (account_id, org_id, user_id, granted_by_user_id, granted_at)
		VALUES ($1, $2, $3, 'admin', $4)`, accountID, org, user, at)
}

func (f *credFixture) workspace(org, external string) {
	f.t.Helper()
	f.exec(`INSERT INTO org_provider_workspaces (org_id, provider, external_org_id, display_name, bound_by_user_id)
		VALUES ($1, 'claude', $2, 'Acme Team', 'admin')`, org, external)
}

// orgConnection inserts an org connection with one secret sealed under the
// vault key vk of the org's tenant, an audit row and a pending connect.
func (f *credFixture) orgConnection(id, org string) {
	f.t.Helper()
	f.exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk-' || $1, 'org', $2, 1, 'v1', '\x01', 'primary')`, id, org)
	f.exec(`INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ($1, 'org', 'admin', $2, 'slack', 'api_key', $1, 'active')`, id, org)
	f.exec(`INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext)
		VALUES ($1, 'api_key', 'vk-' || $1, '\x01')`, id)
	f.exec(`INSERT INTO connection_events (connection_id, user_id, kind, actor) VALUES ($1, 'admin', 'created', 'user:admin')`, id)
	f.oauthFlow(id, org)
}

func (f *credFixture) oauthFlow(id, org string) {
	f.t.Helper()
	f.exec(`INSERT INTO oauth_flows (state_hash, user_id, session_id_hash, integration_id, pkce_verifier_sealed, expires_at, org_id)
		VALUES (convert_to($1, 'UTF8'), 'admin', '\x01', 'slack', '\x01', now() + interval '10 minutes', $2)`, "state-"+id, org)
}

func TestListProviderAccountGrantMembers_PagesDistinctPairsInOrder(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.orgKey("k-a1", "org-a")
	f.orgKey("k-a2", "org-a")
	f.orgKey("k-b", "org-b")
	now := credT0
	f.grant("k-a1", "org-a", "u1", now)
	f.grant("k-a2", "org-a", "u1", now) // the same member twice: one pair
	f.grant("k-a1", "org-a", "u2", now)
	f.grant("k-b", "org-b", "u1", now)

	var got []core.GrantMember
	after := core.GrantMember{}
	for {
		page, err := store.ListProviderAccountGrantMembers(ctx, f.env.ORM, after, 2)
		require.NoError(t, err)
		got = append(got, page...)
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1]
	}
	assert.Equal(t, []core.GrantMember{
		{OrgID: "org-a", UserID: "u1"}, {OrgID: "org-a", UserID: "u2"}, {OrgID: "org-b", UserID: "u1"},
	}, got)
}

func TestDeleteProviderAccountGrantsBefore_DeletesOnlyThatMembersOlderGrants(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.orgKey("k-a1", "org-a")
	f.orgKey("k-a2", "org-a")
	f.orgKey("k-b", "org-b")
	since := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	f.grant("k-a1", "org-a", "u1", since.Add(-time.Hour)) // an earlier membership's
	f.grant("k-a2", "org-a", "u1", since)                 // made as the membership began: counts
	f.grant("k-a1", "org-a", "u2", since.Add(-time.Hour)) // another member's
	f.grant("k-b", "org-b", "u1", since.Add(-time.Hour))  // the same person in another org

	n, err := store.DeleteProviderAccountGrantsBefore(ctx, f.env.ORM, "org-a", "u1", since)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM provider_account_grants WHERE account_id = 'k-a1' AND user_id = 'u1'`))
	assert.Equal(t, 3, f.count(`SELECT count(*) FROM provider_account_grants`))
}

// Each source of an org's credentials makes the org a holder, and the purge
// clears every one of them: a source the list names but the purge leaves
// would be asked about forever, and one the purge clears but the list misses
// would never be purged.
func TestOrgCredentialHoldersAreWhatThePurgeClears(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.orgKey("k-key", "o-key")
	f.personal("p-seat", "member", "o-seat", "workspace")
	f.orgKey("k-grant-host", "o-key")
	f.grant("k-grant-host", "o-grant", "member", credT0)
	f.workspace("o-ws", "claude-org-uuid")
	f.exec(`INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ('c-conn', 'org', 'admin', 'o-conn', 'slack', 'api_key', 'Bot', 'active')`)
	f.oauthFlow("flow", "o-flow")
	f.exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk-only', 'org', 'o-vault', 1, 'v1', '\x01', 'primary')`)
	// A person's own rows make no org a holder.
	f.personal("p-plain", "member", "", "")
	f.exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk-user', 'user', 'member', 1, 'v1', '\x01', 'primary')`)

	holders := func() []string {
		var all []string
		after := ""
		for {
			page, err := store.ListOrgCredentialHolders(ctx, f.env.ORM, after, 3)
			require.NoError(t, err)
			all = append(all, page...)
			if len(page) < 3 {
				return all
			}
			after = page[len(page)-1]
		}
	}
	all := []string{"o-conn", "o-flow", "o-grant", "o-key", "o-seat", "o-vault", "o-ws"}
	require.Equal(t, all, holders())
	for _, org := range all {
		_, err := store.PurgeOrgCredentials(ctx, f.env.ORM, org)
		require.NoError(t, err, org)
		assert.NotContains(t, holders(), org, "purging %s clears every source that named it", org)
	}
	assert.Empty(t, holders())
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM provider_accounts WHERE id IN ('p-plain', 'p-seat') AND attributed_org_id IS NULL`),
		"a person's own account stays, and so does a released seat")
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM vault_keys WHERE tenant_kind = 'user'`), "a person's tenant is untouched")
}

// The purge removes everything of the gone org and nothing of another org or
// of a person, and a second run finds nothing.
func TestPurgeOrgCredentials_RemovesTheOrgsAndOnlyTheOrgs(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	now := credT0
	for _, org := range []string{"org-gone", "org-live"} {
		f.orgKey("k-"+org, org)
		f.grant("k-"+org, org, "member", now)
		f.workspace(org, "ws-"+org)
		f.orgConnection("c-"+org, org)
		f.personal("seat-"+org, "member", org, "workspace")
		f.personal("managed-"+org, "member", org, "managed")
	}
	f.personal("mine", "member", "", "")
	// A thread on each org's key: the gone one's assignment cascades.
	require.NoError(t, store.CreateChat(ctx, f.env.ORM, &store.Chat{
		ID: "chat-1", Title: "c", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	f.exec(`INSERT INTO threads (id, chat_id, origin, created_at) VALUES ('thread-1', 'chat-1', 'main', now())`)
	f.exec(`INSERT INTO thread_provider_accounts (thread_id, provider, account_id, assigned_at, reason)
		VALUES ('thread-1', 'anthropic', 'k-org-gone', now(), 'initial'),
		       ('thread-1', 'openrouter', 'k-org-live', now(), 'initial')`)

	purged, err := store.PurgeOrgCredentials(ctx, f.env.ORM, "org-gone")
	require.NoError(t, err)
	assert.Equal(t, core.OrgCredentialsPurged{
		ProviderAccounts: 1, Grants: 1, Workspaces: 1, Attributions: 1, ManagedKeys: 1,
		Connections: 1, ConnectionEvents: 1, OAuthFlows: 1, VaultKeys: 1,
	}, purged)

	assert.Equal(t, 0, f.count(`SELECT count(*) FROM provider_accounts WHERE org_id = 'org-gone' OR attributed_org_id = 'org-gone'`))
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM provider_accounts WHERE id = 'managed-org-gone'`), "the gone org's managed key is deleted")
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM provider_accounts WHERE id = 'seat-org-gone' AND user_id = 'member' AND attribution_source IS NULL`),
		"the seat is the person's login: released, not deleted")
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM thread_provider_accounts WHERE account_id = 'k-org-gone'`), "no thread stays on a purged key")
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM thread_provider_accounts WHERE account_id = 'k-org-live'`))
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM connection_secrets WHERE connection_id = 'c-org-gone'`))
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM vault_keys WHERE tenant_kind = 'org' AND tenant_id = 'org-gone'`))

	// The live org keeps all of it; the person keeps their own.
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM provider_accounts WHERE org_id = 'org-live'`))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM provider_accounts WHERE attributed_org_id = 'org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM provider_account_grants WHERE org_id = 'org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM org_provider_workspaces WHERE org_id = 'org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM connection_secrets WHERE connection_id = 'c-org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM oauth_flows WHERE org_id = 'org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM vault_keys WHERE tenant_id = 'org-live'`))
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM provider_accounts WHERE id = 'mine'`))

	again, err := store.PurgeOrgCredentials(ctx, f.env.ORM, "org-gone")
	require.NoError(t, err)
	assert.Zero(t, again.Total(), "a repeat finds nothing")

	_, err = store.PurgeOrgCredentials(ctx, f.env.ORM, "")
	assert.Error(t, err, "an empty org id is a caller's bug, never a silent no-op")
}
