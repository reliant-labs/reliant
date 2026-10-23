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
	repo.EnableCredentialSealing(vault.New(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)}))
	return repo, raw
}

// allowLegacyRows removes the contract CHECK so a test can plant the NULL-sealed
// rows a pre-contract database may still hold when the migration runs.
func allowLegacyRows(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.Exec(`ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_sealed_not_null`)
	require.NoError(t, err)
}

func TestSetAutomationTokenWritesSealedColumn(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-plain"))

	var plain string
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT api_key, api_key_sealed FROM api_keys WHERE user_id='u1' AND provider='reliant-automation:anthropic'`).Scan(&plain, &sealed))
	require.Equal(t, "", plain, "contract: plaintext column is never written")
	require.NotEmpty(t, sealed, "sealed column must be written")
	require.NotContains(t, string(sealed), "sk-plain")
}

func TestGetAutomationTokenIgnoresPlaintextColumn(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-real"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED' WHERE user_id='u1'`)
	require.NoError(t, err)

	got, err := repo.GetAutomationToken(ctx, "u1", "anthropic")
	require.NoError(t, err)
	require.Equal(t, "sk-real", got)
}

func TestSealedAPIKeyDoesNotOpenWhenTransplanted(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-u1"))
	require.NoError(t, repo.SetAutomationToken(ctx, "u2", "anthropic", "sk-u2"))
	// Copy u1's ciphertext over u2's row.
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed=(SELECT api_key_sealed FROM api_keys WHERE user_id='u1')
		WHERE user_id='u2'`)
	require.NoError(t, err)
	_, err = repo.GetAutomationToken(ctx, "u2", "anthropic")
	require.Error(t, err, "a transplanted ciphertext must not open")
}

func TestBackfillSealsLegacyRowsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	allowLegacyRows(t, raw)
	for i, p := range []string{"reliant-automation:openai", "reliant-automation:anthropic", "reliant-automation:google"} {
		_, err := raw.ExecContext(ctx,
			`INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ($1, $2, $3, $4)`,
			"legacy-"+p, "u"+string(rune('1'+i%2)), p, "legacy-"+p)
		require.NoError(t, err)
	}
	n, err := repo.countUnsealedAPIKeys(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	// batch of 2 forces more than one pass.
	sealed, err := repo.backfillAPIKeys(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 3, sealed)
	n, err = repo.countUnsealedAPIKeys(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "count(api_key_sealed IS NULL) must reach zero")

	var before []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE id='legacy-reliant-automation:openai'`).Scan(&before))

	sealed, err = repo.backfillAPIKeys(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 0, sealed, "second run is a no-op")
	var after []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE id='legacy-reliant-automation:openai'`).Scan(&after))
	require.Equal(t, before, after, "idempotent: existing ciphertext is not re-sealed")

	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED'`)
	require.NoError(t, err)
	got, err := repo.GetAutomationToken(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "legacy-reliant-automation:openai", got, "backfilled value is served from the sealed column")
}

func TestUnsealedRowDoesNotFallBackToPlaintext(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	allowLegacyRows(t, raw)
	_, err := raw.ExecContext(ctx, `INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('l1','u1','reliant-automation:openai','legacy')`)
	require.NoError(t, err)
	_, err = repo.GetAutomationToken(ctx, "u1", "openai")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "legacy")
}

func TestClearedSealedColumnErrorsInsteadOfFallingBack(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-real"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='sk-plain-decoy', api_key_sealed=NULL WHERE user_id='u1'`)
	// The contract CHECK may reject the NULL; either way no fallback is possible.
	if err == nil {
		_, err = repo.GetAutomationToken(ctx, "u1", "anthropic")
		require.Error(t, err)
		return
	}
	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key='sk-plain-decoy', api_key_sealed='\x'::bytea WHERE user_id='u1'`)
	require.NoError(t, err)
	_, err = repo.GetAutomationToken(ctx, "u1", "anthropic")
	require.Error(t, err)
}

func TestSetAutomationTokenRequiresVault(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	bare, err := NewRepoWithDriver(raw, DriverPostgres)
	require.NoError(t, err)
	require.Error(t, bare.SetAutomationToken(context.Background(), "u1", "openai", "sk"))
	_, err = bare.GetAutomationToken(context.Background(), "u1", "openai")
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
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	allowLegacyRows(t, raw)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	real := vault.New(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)})
	_, err = raw.ExecContext(ctx, `INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('l1','u1','reliant-automation:openai','legacy')`)
	require.NoError(t, err)

	err = sealAPIKeys(ctx, repo, racingSealer{CredentialSealerForTest: real, raw: raw})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to start")

	// Healthy path: a normal sealer drains the row and boot succeeds.
	_, err = raw.ExecContext(ctx, `ALTER TABLE api_keys ADD CONSTRAINT api_keys_sealed_not_null CHECK (api_key_sealed IS NOT NULL) NOT VALID`)
	require.NoError(t, err)
	require.NoError(t, sealAPIKeys(ctx, repo, real))
	got, err := repo.GetAutomationToken(ctx, "u1", "openai")
	require.NoError(t, err)
	require.Equal(t, "legacyx", got)
}

func TestAutomationCredResolverEndToEndOverSealedStore(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	const user, daemon = "u1", "daemon-1"
	require.NoError(t, repo.SetAutomationToken(ctx, user, daemon, "rlat_secret"))

	var plain string
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key, api_key_sealed FROM api_keys WHERE user_id=$1`, user).Scan(&plain, &sealed))
	require.NotEmpty(t, sealed)
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key='TAMPERED'`)
	require.NoError(t, err)

	bearer, err := automationcred.New(automationcred.Deps{Keys: repo}).BearerFor(automationcred.Allow(ctx), user, daemon)
	require.NoError(t, err)
	require.Equal(t, "rlat_secret", bearer)

	other, err := automationcred.New(automationcred.Deps{Keys: repo}).BearerFor(automationcred.Allow(ctx), user, "daemon-2")
	require.NoError(t, err)
	require.Equal(t, "", other, "another daemon's token is not served")
}
