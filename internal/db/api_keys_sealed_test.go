// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io/fs"
	"strings"
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
	require.NoError(t, repo.EnableCredentialSealing(vault.New(raw, vault.NewEnvKeyWrapper(ring))))
	return repo, raw
}

// allowLegacyRows removes the contract CHECK so a test can plant the NULL-sealed
// rows a pre-contract database may still hold when the migration runs.
func allowLegacyRows(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.Exec(`ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_sealed_not_null`)
	require.NoError(t, err)
}

func TestSetProviderAPIKeyWritesSealedColumn(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-plain"))

	var plain string
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT api_key, api_key_sealed FROM api_keys WHERE user_id='u1' AND provider='anthropic'`).Scan(&plain, &sealed))
	require.Equal(t, "", plain, "contract: plaintext column is never written")
	require.NotEmpty(t, sealed, "sealed column must be written")
	require.NotContains(t, string(sealed), "sk-plain")
}

func TestGetProviderAPIKeyIgnoresPlaintextColumn(t *testing.T) {
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
	allowLegacyRows(t, raw)
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

func TestUnsealedRowDoesNotFallBackToPlaintext(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	allowLegacyRows(t, raw)
	_, err := raw.ExecContext(ctx, `INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('l1','u1','openai','legacy')`)
	require.NoError(t, err)
	_, err = repo.GetProviderAPIKey(ctx, "u1", "openai")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "legacy")
	_, err = repo.GetProviderAPIKeys(ctx, "u1")
	require.Error(t, err)
}

func TestClearedSealedColumnErrorsInsteadOfFallingBack(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk-real"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='sk-plain-decoy', api_key_sealed=NULL WHERE user_id='u1'`)
	// The contract CHECK may reject the NULL; either way no fallback is possible.
	if err == nil {
		_, err = repo.GetProviderAPIKey(ctx, "u1", "anthropic")
		require.Error(t, err)
		return
	}
	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key='sk-plain-decoy', api_key_sealed='\x'::bytea WHERE user_id='u1'`)
	require.NoError(t, err)
	_, err = repo.GetProviderAPIKey(ctx, "u1", "anthropic")
	require.Error(t, err)
}

func TestSetProviderAPIKeyRequiresVault(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	bare := NewRepoWithDriver(raw, DriverPostgres)
	require.Error(t, bare.SetProviderAPIKey(context.Background(), "u1", "openai", "sk"))
	_, err := bare.GetProviderAPIKey(context.Background(), "u1", "openai")
	require.Error(t, err)
}

// racingSealer rewrites the plaintext of every row it seals, so the backfill's
// compare-and-set on api_key can never apply.
type racingSealer struct {
	CredentialSealerForTest
	raw *sql.DB
}

func (r racingSealer) Seal(ctx context.Context, tn vault.Tenant, pt, aad []byte) ([]byte, error) {
	if _, err := r.raw.ExecContext(ctx, `UPDATE api_keys SET api_key = api_key || 'x' WHERE api_key_sealed IS NULL`); err != nil {
		return nil, err
	}
	return r.CredentialSealerForTest.Seal(ctx, tn, pt, aad)
}

type CredentialSealerForTest interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

func TestBootRefusesWhenUnsealedRowRemainsAfterBackfill(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	allowLegacyRows(t, raw)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	real := vault.New(raw, vault.NewEnvKeyWrapper(ring))
	_, err = raw.ExecContext(ctx, `INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('l1','u1','openai','legacy')`)
	require.NoError(t, err)

	err = sealAPIKeys(ctx, repo, racingSealer{CredentialSealerForTest: real, raw: raw})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to start")

	// Healthy path: a normal sealer drains the row and boot succeeds.
	_, err = raw.ExecContext(ctx, `ALTER TABLE api_keys ADD CONSTRAINT api_keys_sealed_not_null CHECK (api_key_sealed IS NOT NULL) NOT VALID`)
	require.NoError(t, err)
	require.NoError(t, sealAPIKeys(ctx, repo, real))
	got, err := repo.GetProviderAPIKey(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "legacyx", got)
}

func runContractMigration(t *testing.T, raw *sql.DB) {
	t.Helper()
	body, err := fs.ReadFile(FS, "migrations/postgres/20261004193347_api_keys_contract.sql")
	require.NoError(t, err)
	_, err = raw.Exec(strings.TrimPrefix(string(body), "-- +goose Up"))
	require.NoError(t, err)
}

func TestContractMigrationToleratesLegacyNullRow(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	// Rewind to the pre-contract state: no constraint, one unsealed and one sealed row.
	_, err := raw.Exec(`ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_sealed_not_null`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('legacy','u1','openai','plain-legacy')`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed) VALUES ('done','u1','google','plain-done','\x01')`)
	require.NoError(t, err)

	runContractMigration(t, raw)
	runContractMigration(t, raw) // idempotent

	var legacy, done string
	require.NoError(t, raw.QueryRow(`SELECT api_key FROM api_keys WHERE id='legacy'`).Scan(&legacy))
	require.NoError(t, raw.QueryRow(`SELECT api_key FROM api_keys WHERE id='done'`).Scan(&done))
	require.Equal(t, "plain-legacy", legacy, "unsealed rows keep plaintext so the backfill can seal them")
	require.Equal(t, "", done, "sealed rows are blanked")

	_, err = raw.Exec(`INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('new','u2','openai','x')`)
	require.Error(t, err, "NOT VALID check still rejects new NULL rows")
}

func TestContractMigrationOnCleanDB(t *testing.T) {
	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "u1", "anthropic", "sk"))
	_, err := raw.Exec(`UPDATE api_keys SET api_key='leftover'`)
	require.NoError(t, err)
	runContractMigration(t, raw)
	var plain string
	require.NoError(t, raw.QueryRow(`SELECT api_key FROM api_keys`).Scan(&plain))
	require.Equal(t, "", plain)
	require.NoError(t, repo.ValidateAPIKeysSealedConstraint(ctx))
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
