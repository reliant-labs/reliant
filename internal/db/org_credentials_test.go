// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// A gone org's purge (control-plane docs/design/org-provider-accounts.md §3.5,
// §6, "Org deleted") leaves no ciphertext of the org that opens: its rows are
// deleted, and every blob sealed under its tenant — copied out beforehand, as
// a backup or a stray row would hold it — no longer opens, because the
// tenant's data keys are gone. Another org's and the person's own still open.
func TestPurgeOrgCredentials_LeavesNoOpenableCiphertext(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	// One KEK for the repo's vault and for a second vault over the same
	// database, which models every other process (and a restore): it has
	// none of the first one's unwrapped keys cached.
	keyBytes := make([]byte, 32)
	_, err := rand.Read(keyBytes)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(keyBytes))
	require.NoError(t, err)
	repo.EnableCredentialSealing(vault.NewInstrumented(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)}))
	fresh := func() vault.Service {
		return vault.NewInstrumented(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)})
	}

	gone := mustUpsert(t, repo, orgKeyUpsert("org-gone", "admin", "acme", "sk-ant-gone-0001"))
	live := mustUpsert(t, repo, orgKeyUpsert("org-live", "admin", "other", "sk-ant-live-0002"))
	mine := mustUpsert(t, repo, apiKeyUpsert("admin", "mine", "sk-ant-mine-0003"))

	// An org connection's secret, sealed under the gone org's tenant.
	connAAD := []byte("connection_secrets\x00c-gone\x00api_key")
	connBlob, err := repo.accounts.sealer.Seal(ctx, vault.OrgTenant("org-gone"), []byte("xoxb-gone"), connAAD)
	require.NoError(t, err)
	keyID, err := vault.KeyIDOf(connBlob)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ('c-gone', 'org', 'admin', 'org-gone', 'slack', 'api_key', 'Bot', 'active')`)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext)
		VALUES ('c-gone', 'api_key', $1, $2)`, keyID, connBlob)
	require.NoError(t, err)

	sealedOf := func(id string) []byte {
		var b []byte
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT secret_sealed FROM provider_accounts WHERE id = $1`, id).Scan(&b))
		return b
	}
	goneBlob, liveBlob, mineBlob := sealedOf(gone.ID), sealedOf(live.ID), sealedOf(mine.ID)
	goneAAD := ProviderAccountAAD(gone.ID, accountColSecret)
	_, err = fresh().Open(ctx, vault.OrgTenant("org-gone"), goneBlob, goneAAD)
	require.NoError(t, err, "before the purge the org's key opens")

	purged, err := repo.PurgeOrgCredentials(ctx, "org-gone")
	require.NoError(t, err)
	assert.EqualValues(t, 1, purged.ProviderAccounts)
	assert.EqualValues(t, 1, purged.Connections)
	assert.EqualValues(t, 1, purged.VaultKeys)

	var rows int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM provider_accounts WHERE org_id = 'org-gone')
		+ (SELECT count(*) FROM connection_secrets WHERE connection_id = 'c-gone')
		+ (SELECT count(*) FROM vault_keys WHERE tenant_kind = 'org' AND tenant_id = 'org-gone')`).Scan(&rows))
	assert.Zero(t, rows, "no row of the gone org is left to hold a secret")

	v := fresh()
	_, err = v.Open(ctx, vault.OrgTenant("org-gone"), goneBlob, goneAAD)
	assert.ErrorIs(t, err, vault.ErrKeyNotFound, "the org key's ciphertext no longer opens")
	_, err = v.Open(ctx, vault.OrgTenant("org-gone"), connBlob, connAAD)
	assert.ErrorIs(t, err, vault.ErrKeyNotFound, "the org connection's ciphertext no longer opens")

	plain, err := v.Open(ctx, vault.OrgTenant("org-live"), liveBlob, ProviderAccountAAD(live.ID, accountColSecret))
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-live-0002", string(plain), "another org's key still opens")
	plain, err = v.Open(ctx, vault.UserTenant("admin"), mineBlob, ProviderAccountAAD(mine.ID, accountColSecret))
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-mine-0003", string(plain), "the creator's own key still opens")
	_, err = repo.GetOwnedProviderAccountWithSecrets(ctx, core.OrgAccountOwner("org-gone"), gone.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
}
