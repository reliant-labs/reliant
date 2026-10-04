// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/stretchr/testify/require"
)

func sealedRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	require.NoError(t, repo.EnableAPIKeySealing(vault.New(raw, vault.NewEnvKeyWrapper(ring))))
	return repo, raw
}

func TestSetProviderAPIKeyWritesSealedColumn(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-plain"))

	var plain string
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT api_key, api_key_sealed FROM api_keys WHERE user_id='u1' AND provider='anthropic'`).Scan(&plain, &sealed))
	require.Equal(t, "sk-plain", plain, "expand phase dual-writes the plaintext column")
	require.NotEmpty(t, sealed, "sealed column must be written")
	require.NotContains(t, string(sealed), "sk-plain")
}

func TestGetProviderAPIKeyPrefersSealedOverPlaintext(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-real"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED' WHERE user_id='u1'`)
	require.NoError(t, err)

	got, err := repo.GetProviderAPIKey(ctx, "u1", "anthropic")
	require.NoError(t, err)
	require.Equal(t, "sk-real", got)

	all, err := repo.GetProviderAPIKeys(ctx, "u1")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"anthropic": "sk-real"}, all)
}

func TestSealedAPIKeyDoesNotOpenWhenTransplanted(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-u1"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u2", "anthropic", "sk-u2"))
	// Copy u1's ciphertext over u2's row.
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed=(SELECT api_key_sealed FROM api_keys WHERE user_id='u1')
		WHERE user_id='u2'`)
	require.NoError(t, err)
	_, err = repo.GetProviderAPIKey(ctx, "u2", "anthropic")
	require.Error(t, err, "a transplanted ciphertext must not open")
}

func TestBackfillSealsLegacyRowsAndIsIdempotent(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	for i, p := range []string{"openai", "anthropic", "google"} {
		_, err := raw.ExecContext(ctx,
			`INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ($1, $2, $3, $4)`,
			"legacy-"+p, "u"+string(rune('1'+i%2)), p, "legacy-"+p)
		require.NoError(t, err)
	}
	n, err := repo.CountUnsealedAPIKeys(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	// batch of 2 forces more than one pass.
	sealed, err := repo.BackfillAPIKeys(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 3, sealed)
	n, err = repo.CountUnsealedAPIKeys(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "count(api_key_sealed IS NULL) must reach zero")

	var before []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE id='legacy-openai'`).Scan(&before))

	sealed, err = repo.BackfillAPIKeys(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 0, sealed, "second run is a no-op")
	var after []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE id='legacy-openai'`).Scan(&after))
	require.Equal(t, before, after, "idempotent: existing ciphertext is not re-sealed")

	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED'`)
	require.NoError(t, err)
	got, err := repo.GetProviderAPIKey(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "legacy-openai", got, "backfilled value is served from the sealed column")
}

func TestLegacyPlaintextRowStillReadableBeforeBackfill(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	_, err := raw.ExecContext(ctx, `INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('l1','u1','openai','legacy')`)
	require.NoError(t, err)
	got, err := repo.GetProviderAPIKey(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "legacy", got)
}

func TestSealedKeysStillHideAutomationPrefix(t *testing.T) {
	repo, _ := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-real"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", core.AutomationProviderPrefix+"daemon-1", "rlat_secret"))

	all, err := repo.GetProviderAPIKeys(ctx, "u1")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"anthropic": "sk-real"}, all)
}

func TestAutomationCredResolverEndToEndOverSealedStore(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	const user, daemon = "u1", "daemon-1"
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, automationcred.Provider(daemon), "rlat_secret"))

	var plain string
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key, api_key_sealed FROM api_keys WHERE user_id=$1`, user).Scan(&plain, &sealed))
	require.NotEmpty(t, sealed)
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED'`)
	require.NoError(t, err)

	bearer, err := automationcred.NewResolver(repo).BearerFor(automationcred.Allow(ctx), user, daemon)
	require.NoError(t, err)
	require.Equal(t, "rlat_secret", bearer)

	other, err := automationcred.NewResolver(repo).BearerFor(automationcred.Allow(ctx), user, "daemon-2")
	require.NoError(t, err)
	require.Equal(t, "", other, "another daemon's token is not served")
}

func TestUnsealedStoreStillWorksWithoutVault(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "openai", "sk"))
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE user_id='u1'`).Scan(&sealed))
	require.Nil(t, sealed)
	got, err := repo.GetProviderAPIKey(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "sk", got)
}
