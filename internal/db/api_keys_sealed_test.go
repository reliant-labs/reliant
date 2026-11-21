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

func TestSetAutomationTokenWritesSealedColumn(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-plain"))

	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT api_key_sealed FROM api_keys WHERE user_id='u1' AND provider='reliant-automation:anthropic'`).Scan(&sealed))
	require.NotEmpty(t, sealed, "sealed column must be written")
	require.NotContains(t, string(sealed), "sk-plain")
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

func TestClearedSealedColumnErrors(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.SetAutomationToken(ctx, "u1", "anthropic", "sk-real"))
	// The validated api_keys_sealed_not_null check refuses a row with no sealed value.
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed=NULL WHERE user_id='u1'`)
	require.Error(t, err)
	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed='\x'::bytea WHERE user_id='u1'`)
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

func TestAutomationCredResolverEndToEndOverSealedStore(t *testing.T) {
	t.Parallel()

	repo, raw := sealedRepo(t)
	ctx := context.Background()
	const user, daemon = "u1", "daemon-1"
	require.NoError(t, repo.SetAutomationToken(ctx, user, daemon, "rlat_secret"))

	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT api_key_sealed FROM api_keys WHERE user_id=$1`, user).Scan(&sealed))
	require.NotEmpty(t, sealed)

	bearer, err := automationcred.New(automationcred.Deps{Keys: repo}).BearerFor(automationcred.Allow(ctx), user, daemon)
	require.NoError(t, err)
	require.Equal(t, "rlat_secret", bearer)

	other, err := automationcred.New(automationcred.Deps{Keys: repo}).BearerFor(automationcred.Allow(ctx), user, "daemon-2")
	require.NoError(t, err)
	require.Equal(t, "", other, "another daemon's token is not served")
}
