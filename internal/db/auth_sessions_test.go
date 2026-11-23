// Copyright (c) 2025 Reliant Labs
package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/stretchr/testify/require"
)

// The login broker keeps its sign-in sessions in auth_sessions through forge's
// Postgres session store (app.NewSignInSessions: session.NewPostgresStore over
// this Repo's pool). These tests run that store over the schema reliant's own
// migrations build (db/migrations/20261123231707_create_auth_sessions.up.sql),
// so a migration that is dropped, renamed or drifts from the store's columns
// fails here, not at a person's first sign-in. The broker's own tests use the
// in-memory store and cannot see the table.

// signInSessions is the Manager app.NewSignInSessions builds, over a fresh
// migrated database. A refresh is never expected (every token outlives the
// test); one that happens fails the test.
func signInSessions(t *testing.T) (*session.Manager, *Repo) {
	t.Helper()
	repo := NewTestRepo(t)
	m, err := session.NewManager(session.Config{
		Store: session.NewPostgresStore(repo.ORM().DB()),
		Refresher: session.RefresherFunc(func(context.Context, string) (session.Tokens, error) {
			t.Error("a token that outlives the test was refreshed")
			return session.Tokens{}, errors.New("no refresh in this test")
		}),
		// Each lookup reaches the table, not the Manager's read cache.
		CacheTTL: time.Nanosecond,
	})
	require.NoError(t, err)
	return m, repo
}

func countSessions(t *testing.T, repo *Repo, subject string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.ORM().DB().QueryRow(`SELECT count(*) FROM auth_sessions WHERE subject = $1`, subject).Scan(&n))
	return n
}

// A session the broker creates is stored in auth_sessions, resolves to its
// tokens by the cookie's id, is listed for its subject and, revoked, is gone:
// the cookie that named it resolves to nothing.
func TestAuthSessions_PostgresStoreCreateLookupRevoke(t *testing.T) {
	ctx := context.Background()
	m, repo := signInSessions(t)
	const subject = "a1b2c3d4-0000-4000-8000-000000000001"
	tokens := session.Tokens{
		AccessToken:  "access-token-plaintext",
		RefreshToken: "refresh-token-plaintext",
		IDToken:      "id-token-plaintext",
		Expiry:       time.Now().Add(24 * time.Hour),
	}

	issued, err := m.Create(ctx, tokens, session.Meta{Subject: subject, UserAgent: "test-agent", IssuerSession: "idp-session-1"})
	require.NoError(t, err)
	require.True(t, session.IsID(issued.ID), "the cookie holds an opaque session id")
	require.Equal(t, 1, countSessions(t, repo, subject), "the session is a row of auth_sessions")

	got, err := m.AccessToken(ctx, issued.ID)
	require.NoError(t, err)
	require.Equal(t, tokens.AccessToken, got)

	info, err := m.Info(ctx, issued.ID)
	require.NoError(t, err)
	require.Equal(t, subject, info.Subject)
	require.Equal(t, "test-agent", info.UserAgent)
	require.False(t, info.ExpiresAt.IsZero())
	listed, err := m.List(ctx, subject)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, issued.Handle, listed[0].Handle)

	// What the table holds signs no one in: the row is keyed by a hash of
	// the id, and the tokens are sealed.
	var idHash, sealed []byte
	var issuerSession string
	require.NoError(t, repo.ORM().DB().QueryRow(
		`SELECT id_hash, sealed, issuer_session FROM auth_sessions WHERE subject = $1`, subject).Scan(&idHash, &sealed, &issuerSession))
	require.Equal(t, "idp-session-1", issuerSession)
	require.False(t, bytes.Contains(idHash, []byte(issued.ID)), "the row is keyed by the id itself")
	for _, secret := range []string{tokens.AccessToken, tokens.RefreshToken, tokens.IDToken} {
		require.False(t, bytes.Contains(sealed, []byte(secret)), "a token is stored in the clear")
	}

	revoked, _, err := m.Revoke(ctx, issued.ID)
	require.NoError(t, err)
	require.Equal(t, tokens.RefreshToken, revoked.RefreshToken, "revoke hands back the tokens to end at the issuer")
	require.Equal(t, 0, countSessions(t, repo, subject))
	_, err = m.AccessToken(ctx, issued.ID)
	require.ErrorIs(t, err, session.ErrNoSession)
}

// Signing a person out everywhere, and the hourly sweep of ended sessions,
// run against the table's subject and expiry columns.
func TestAuthSessions_PostgresStoreRevokeSubjectAndSweep(t *testing.T) {
	ctx := context.Background()
	repo := NewTestRepo(t)
	now := time.Now()
	m, err := session.NewManager(session.Config{
		Store: session.NewPostgresStore(repo.ORM().DB()),
		Refresher: session.RefresherFunc(func(context.Context, string) (session.Tokens, error) {
			return session.Tokens{}, errors.New("no refresh")
		}),
		Lifetime: time.Hour,
		CacheTTL: time.Nanosecond,
		Now:      func() time.Time { return now },
	})
	require.NoError(t, err)
	tokens := session.Tokens{AccessToken: "a", RefreshToken: "r", Expiry: now.Add(24 * time.Hour)}

	for range 2 {
		_, err := m.Create(ctx, tokens, session.Meta{Subject: "everywhere"})
		require.NoError(t, err)
	}
	ended, err := m.Create(ctx, tokens, session.Meta{Subject: "ended"})
	require.NoError(t, err)

	n, err := m.RevokeSubject(ctx, "everywhere")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	require.Equal(t, 0, countSessions(t, repo, "everywhere"))

	now = now.Add(2 * time.Hour) // past the hour's lifetime
	swept, err := m.Sweep(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, swept)
	require.Equal(t, 0, countSessions(t, repo, "ended"))
	_, err = m.AccessToken(ctx, ended.ID)
	require.ErrorIs(t, err, session.ErrNoSession)
}

// The read-only role (db/grants/readonly/public.auth_sessions_gen.sql) may
// read what a session is for — whose, from what agent, when — and never what
// it holds: the hashed id and the sealed tokens are withheld.
func TestAuthSessions_ReadOnlyGrantWithholdsTheSecrets(t *testing.T) {
	raw, _ := NewTestDatabase(t)
	role := fmt.Sprintf("reliant_ro_sess_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	script := embeddedGrantsScript(t)
	require.True(t, strings.Contains(script, "'auth_sessions'"), "the grants script carries no auth_sessions row")
	script = strings.ReplaceAll(script, dbgrants.RoleName, role)
	_, err := raw.Exec(`CREATE ROLE ` + role + ` NOLOGIN`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = raw.Exec(`DROP OWNED BY ` + role)
		_, _ = raw.Exec(`DROP ROLE IF EXISTS ` + role)
	})
	require.NoError(t, dbgrants.Apply(context.Background(), raw, script))

	canRead := func(column string) bool {
		t.Helper()
		var ok bool
		require.NoError(t, raw.QueryRow(`SELECT has_column_privilege($1, 'public.auth_sessions', $2, 'SELECT')`, role, column).Scan(&ok))
		return ok
	}
	for _, column := range []string{"subject", "user_agent", "issuer_session", "created_at", "proved_at", "last_seen_at", "refreshed_at", "idle_expires_at", "expires_at"} {
		require.True(t, canRead(column), "%s is declared readable", column)
	}
	for _, column := range []string{"id_hash", "sealed"} {
		require.False(t, canRead(column), "%s is forge:secret and must be withheld", column)
	}
}
