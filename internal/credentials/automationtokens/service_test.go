// Copyright (c) 2025 Reliant Labs

package automationtokens_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/credentials/automationtokens"
	"github.com/reliant-labs/reliant/internal/db"
)

func newStore(t *testing.T) (automationtokens.Service, *sql.DB) {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	svc, err := automationtokens.New(automationtokens.Deps{DB: repo.ORM(), Sealer: db.NewTestVault(t, repo)})
	require.NoError(t, err)
	return svc, raw
}

func TestSetWritesSealedColumn(t *testing.T) {
	t.Parallel()
	svc, raw := newStore(t)
	ctx := context.Background()
	require.NoError(t, svc.SetAutomationToken(ctx, "u1", "d1", "sk-plain"))

	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT api_key_sealed FROM api_keys WHERE user_id='u1' AND provider='reliant-automation:d1'`).Scan(&sealed))
	require.NotEmpty(t, sealed)
	require.NotContains(t, string(sealed), "sk-plain")

	got, err := svc.GetAutomationToken(ctx, "u1", "d1")
	require.NoError(t, err)
	require.Equal(t, "sk-plain", got)
}

func TestMissingTokenIsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newStore(t)
	_, err := svc.GetAutomationToken(context.Background(), "u1", "d1")
	require.True(t, svcerr.IsNotFound(err), "automationcred branches on this: %v", err)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}

// A token is read for exactly the (user, daemon) it was stored for: another user, or
// another daemon of the same user, never gets it.
func TestTokenIsNeverReturnedToAnotherUserOrDaemon(t *testing.T) {
	t.Parallel()
	svc, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, svc.SetAutomationToken(ctx, "userA", "d1", "rlat-A"))
	require.NoError(t, svc.SetAutomationToken(ctx, "userB", "d1", "rlat-B"))

	b, err := svc.GetAutomationToken(ctx, "userB", "d1")
	require.NoError(t, err)
	require.Equal(t, "rlat-B", b)
	_, err = svc.GetAutomationToken(ctx, "userB", "d2")
	require.True(t, svcerr.IsNotFound(err))
	_, err = svc.GetAutomationToken(ctx, "userC", "d1")
	require.True(t, svcerr.IsNotFound(err))

	// Deleting user B's token leaves user A's.
	require.NoError(t, svc.DeleteAutomationToken(ctx, "userB", "d1"))
	_, err = svc.GetAutomationToken(ctx, "userB", "d1")
	require.True(t, svcerr.IsNotFound(err))
	a, err := svc.GetAutomationToken(ctx, "userA", "d1")
	require.NoError(t, err)
	require.Equal(t, "rlat-A", a)
}

// A ciphertext copied onto another user's row does not open: the vault binds the tenant
// and the row's identity into it.
func TestTransplantedCiphertextDoesNotOpen(t *testing.T) {
	t.Parallel()
	svc, raw := newStore(t)
	ctx := context.Background()
	require.NoError(t, svc.SetAutomationToken(ctx, "u1", "d1", "sk-u1"))
	require.NoError(t, svc.SetAutomationToken(ctx, "u2", "d1", "sk-u2"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed=(SELECT api_key_sealed FROM api_keys WHERE user_id='u1')
		WHERE user_id='u2'`)
	require.NoError(t, err)
	_, err = svc.GetAutomationToken(ctx, "u2", "d1")
	require.Error(t, err)
}

func TestClearedSealedColumnErrors(t *testing.T) {
	t.Parallel()
	svc, raw := newStore(t)
	ctx := context.Background()
	require.NoError(t, svc.SetAutomationToken(ctx, "u1", "d1", "sk-real"))
	_, err := raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed=NULL WHERE user_id='u1'`)
	require.Error(t, err, "the validated api_keys_sealed_not_null check refuses a row with no sealed value")
	_, err = raw.ExecContext(ctx, `UPDATE api_keys SET api_key_sealed='\x'::bytea WHERE user_id='u1'`)
	require.NoError(t, err)
	_, err = svc.GetAutomationToken(ctx, "u1", "d1")
	require.Error(t, err)
}

func TestEmptyIdentifiersAreRefused(t *testing.T) {
	t.Parallel()
	svc, _ := newStore(t)
	ctx := context.Background()
	require.Error(t, svc.SetAutomationToken(ctx, "", "d1", "x"))
	require.Error(t, svc.SetAutomationToken(ctx, "u1", "", "x"))
	_, err := svc.GetAutomationToken(ctx, "", "d1")
	require.Error(t, err)
	require.Error(t, svc.DeleteAutomationToken(ctx, "u1", ""))
}

func TestNewRefusesToRunWithoutAVault(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	_, err := automationtokens.New(automationtokens.Deps{DB: repo.ORM()})
	require.Error(t, err)
}
