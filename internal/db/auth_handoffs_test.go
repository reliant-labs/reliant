// Copyright (c) 2025 Reliant Labs
package db

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/stretchr/testify/require"
)

// Exercise the store over reliant's migrated schema, never HandoffSchemaSQL:
// removing or drifting the consumer migration must fail before a real login.
func TestAuthHandoffs_PostgresStoreRoundTripAndSweep(t *testing.T) {
	ctx := context.Background()
	repo := NewTestRepo(t)
	store := session.NewPostgresStore(repo.ORM().DB())
	now := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	h := session.Handoff{
		Key: bytes.Repeat([]byte{1}, 32), Sealed: []byte("opaque-encrypted-record"),
		ExpiresAt: now.Add(time.Minute),
	}
	require.NoError(t, store.PutHandoff(ctx, h))
	require.Error(t, store.PutHandoff(ctx, h), "the migration must enforce unique keys")
	got, err := store.GetHandoff(ctx, h.Key)
	require.NoError(t, err)
	require.Equal(t, h.Key, got.Key)
	require.Equal(t, h.Sealed, got.Sealed)
	require.True(t, h.ExpiresAt.Equal(got.ExpiresAt))
	got, err = store.TakeHandoff(ctx, h.Key)
	require.NoError(t, err)
	require.Equal(t, h.Key, got.Key)
	require.Equal(t, h.Sealed, got.Sealed)
	require.True(t, h.ExpiresAt.Equal(got.ExpiresAt))
	_, err = store.TakeHandoff(ctx, h.Key)
	require.ErrorIs(t, err, session.ErrNoHandoff, "a redeemed code cannot be reused")
	_, err = store.GetHandoff(ctx, h.Key)
	require.ErrorIs(t, err, session.ErrNoHandoff)

	require.NoError(t, store.PutHandoff(ctx, h))
	live := h
	live.Key = bytes.Repeat([]byte{2}, 32)
	live.ExpiresAt = now.Add(2 * time.Minute)
	require.NoError(t, store.PutHandoff(ctx, live))
	n, err := store.DeleteExpiredHandoffs(ctx, h.ExpiresAt)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	_, err = store.GetHandoff(ctx, h.Key)
	require.ErrorIs(t, err, session.ErrNoHandoff)
	got, err = store.GetHandoff(ctx, live.Key)
	require.NoError(t, err)
	require.Equal(t, live.Key, got.Key)
	require.Equal(t, live.Sealed, got.Sealed)
	require.True(t, live.ExpiresAt.Equal(got.ExpiresAt), "sweeping must retain unexpired handoffs")
}

// Apply the embedded, generated grants to the actual migrated schema. A
// readable hash or sealed credential is a regression even if the store works.
func TestAuthHandoffs_ReadOnlyGrantWithholdsTheSecrets(t *testing.T) {
	raw, _ := NewTestDatabase(t)
	role := fmt.Sprintf("reliant_ro_handoff_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	script := embeddedGrantsScript(t)
	require.Contains(t, script, "'auth_handoffs'", "the grants script must include the handoff table")
	script = strings.ReplaceAll(script, dbgrants.RoleName, role)
	_, err := raw.Exec(`CREATE ROLE ` + role + ` NOLOGIN`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = raw.Exec(`DROP OWNED BY ` + role)
		_, _ = raw.Exec(`DROP ROLE IF EXISTS ` + role)
	})
	require.NoError(t, dbgrants.Apply(context.Background(), raw, script))

	for _, column := range []string{"created_at", "expires_at", "key_hash", "sealed"} {
		var readable bool
		require.NoError(t, raw.QueryRow(`SELECT has_column_privilege($1, 'public.auth_handoffs', $2, 'SELECT')`, role, column).Scan(&readable))
		require.Equal(t, column == "created_at" || column == "expires_at", readable, "read-only privilege for %s", column)
	}
}
