package tokenauthority

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func TestNewPicksExactlyOneStore(t *testing.T) {
	t.Run("control-plane URL selects the hosted authority", func(t *testing.T) {
		a, mode, err := New(Deps{ControlPlaneURL: "http://cp.invalid", InternalServiceSecret: "s3cret", DB: &sql.DB{}})
		require.NoError(t, err)
		require.Equal(t, ModeControlPlane, mode)
		_, ok := a.(*accesstokenclient.Client)
		require.True(t, ok, "a configured control-plane must win even when a DB is present: got %T", a)
	})

	t.Run("control-plane URL without a secret is an error, never a local fallback", func(t *testing.T) {
		a, _, err := New(Deps{ControlPlaneURL: "http://cp.invalid", DB: &sql.DB{}})
		require.Error(t, err)
		require.Nil(t, a)
		require.Contains(t, err.Error(), "INTERNAL_SERVICE_SECRET")
	})

	t.Run("no control-plane selects the local store", func(t *testing.T) {
		a, mode, err := New(Deps{DB: &sql.DB{}})
		require.NoError(t, err)
		require.Equal(t, ModeLocal, mode)
		_, ok := a.(*LocalStore)
		require.True(t, ok, "got %T", a)
	})

	t.Run("neither is an error", func(t *testing.T) {
		_, _, err := New(Deps{})
		require.Error(t, err)
	})
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", " http://admin-server:8090 ")
	t.Setenv("INTERNAL_SERVICE_SECRET", " s ")
	cfg := DepsFromEnv(nil)
	require.Equal(t, "http://admin-server:8090", cfg.ControlPlaneURL)
	require.Equal(t, "s", cfg.InternalServiceSecret)
}

// Both stores' inactive error is recognized by internal/auth without it
// importing either store.
func TestErrInactiveIsAuthInactive(t *testing.T) {
	require.True(t, errors.Is(ErrInactive, auth.ErrAccessTokenInactive))
}

// TestAuthorityConformance runs ONE behavioural suite against every Authority
// reliant can construct locally. Memory is what the rest of the test suite
// uses in place of the real stores, so this is what makes those tests
// trustworthy: if Memory drifts from LocalStore, this fails.
func TestAuthorityConformance(t *testing.T) {
	t.Run("memory", func(t *testing.T) { runConformance(t, NewMemory()) })
	t.Run("local-postgres", func(t *testing.T) {
		_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
		t.Cleanup(cleanup)
		runConformance(t, NewLocalStore(rawDB))
	})
}

func runConformance(t *testing.T, a Authority) {
	ctx := context.Background()
	user := "user-" + strings.ReplaceAll(t.Name(), "/", "-")

	mint := func(t *testing.T, req MintRequest) Minted {
		t.Helper()
		m, err := a.MintForUser(ctx, req)
		require.NoError(t, err)
		return m
	}

	t.Run("mint then introspect", func(t *testing.T) {
		m := mint(t, MintRequest{UserID: user, Name: "ci", Scopes: []fat.Scope{fat.ScopeReliantAPI}})
		require.True(t, fat.HasFormat(m.Plaintext))
		require.True(t, strings.HasPrefix(m.Plaintext, m.DisplayPrefix))

		p, err := a.Introspect(ctx, m.Plaintext)
		require.NoError(t, err)
		require.Equal(t, m.TokenID, p.TokenID)
		require.Equal(t, user, p.ActingUserID)
		require.True(t, p.Scopes.Permits(fat.ScopeReliantAPI))
		require.False(t, p.Scopes.Permits(fat.ScopeDaemonConnect))
		require.Nil(t, p.Resource)
	})

	t.Run("resource binding round-trips", func(t *testing.T) {
		res := &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-7"}
		m := mint(t, MintRequest{UserID: user, Name: "laptop", Scopes: []fat.Scope{fat.ScopeDaemonConnect}, Resource: res})
		p, err := a.Introspect(ctx, m.Plaintext)
		require.NoError(t, err)
		require.Equal(t, res, p.Resource)
	})

	t.Run("unknown and malformed tokens are inactive", func(t *testing.T) {
		unknown, err := fat.Mint()
		require.NoError(t, err)
		for _, tok := range []string{unknown.Plaintext, "rlnt_pat_legacy", "", "garbage"} {
			_, err := a.Introspect(ctx, tok)
			require.ErrorIs(t, err, auth.ErrAccessTokenInactive, "token %q", tok)
		}
	})

	t.Run("expired token is inactive", func(t *testing.T) {
		soon := time.Now().Add(1500 * time.Millisecond)
		m := mint(t, MintRequest{UserID: user, Name: "short", Scopes: []fat.Scope{fat.ScopeReliantAPI}, ExpiresAt: &soon})
		_, err := a.Introspect(ctx, m.Plaintext)
		require.NoError(t, err)
		time.Sleep(time.Until(soon) + 200*time.Millisecond)
		_, err = a.Introspect(ctx, m.Plaintext)
		require.ErrorIs(t, err, auth.ErrAccessTokenInactive)
	})

	t.Run("grant rules are enforced", func(t *testing.T) {
		_, err := a.MintForUser(ctx, MintRequest{UserID: "", Name: "x", Scopes: []fat.Scope{fat.ScopeReliantAPI}})
		require.True(t, IsInvalidGrant(err), "empty user: %v", err)
		_, err = a.MintForUser(ctx, MintRequest{UserID: user, Name: "x", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
			Resource: &fat.Resource{Kind: fat.ResourceConnector, ID: "c"}})
		require.True(t, IsInvalidGrant(err), "daemon:connect bound to a connector: %v", err)
	})

	t.Run("list is per user, filters by scope, hides revoked", func(t *testing.T) {
		owner := user + "-list"
		api := mint(t, MintRequest{UserID: owner, Name: "api", Scopes: []fat.Scope{fat.ScopeReliantAPI}})
		mint(t, MintRequest{UserID: owner, Name: "daemon", Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
		mint(t, MintRequest{UserID: owner + "-other", Name: "api", Scopes: []fat.Scope{fat.ScopeReliantAPI}})

		all, err := a.ListForUser(ctx, owner, "")
		require.NoError(t, err)
		require.Len(t, all, 2)
		onlyAPI, err := a.ListForUser(ctx, owner, fat.ScopeReliantAPI)
		require.NoError(t, err)
		require.Len(t, onlyAPI, 1)
		require.Equal(t, api.TokenID, onlyAPI[0].ID)
		require.Equal(t, api.DisplayPrefix, onlyAPI[0].DisplayPrefix)

		require.NoError(t, a.RevokeForUser(ctx, owner, api.TokenID))
		all, err = a.ListForUser(ctx, owner, "")
		require.NoError(t, err)
		require.Len(t, all, 1)
	})

	t.Run("revoke is owner-scoped and idempotent", func(t *testing.T) {
		m := mint(t, MintRequest{UserID: user, Name: "revoke-me", Scopes: []fat.Scope{fat.ScopeReliantAPI}})
		require.True(t, IsNotFound(a.RevokeForUser(ctx, "someone-else", m.TokenID)))
		_, err := a.Introspect(ctx, m.Plaintext)
		require.NoError(t, err, "a foreign revoke must not touch the token")

		require.NoError(t, a.RevokeForUser(ctx, user, m.TokenID))
		_, err = a.Introspect(ctx, m.Plaintext)
		require.ErrorIs(t, err, auth.ErrAccessTokenInactive)
		require.NoError(t, a.RevokeForUser(ctx, user, m.TokenID), "revoking twice is not an error")
	})

	t.Run("revoke by resource", func(t *testing.T) {
		res := fat.Resource{Kind: fat.ResourceConnector, ID: "grant-" + user}
		m := mint(t, MintRequest{UserID: user, Name: "conn", Scopes: []fat.Scope{fat.ScopeMCPConnector}, Resource: &res})
		n, err := a.RevokeResource(ctx, res)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		_, err = a.Introspect(ctx, m.Plaintext)
		require.ErrorIs(t, err, auth.ErrAccessTokenInactive)
	})

	t.Run("revoke ephemeral spares durable tokens", func(t *testing.T) {
		owner := user + "-eph"
		hour := time.Now().Add(time.Hour)
		eph := mint(t, MintRequest{UserID: owner, Name: "session", Scopes: []fat.Scope{fat.ScopeDaemonConnect}, Ephemeral: true,
			Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-eph"}, ExpiresAt: &hour})
		durable := mint(t, MintRequest{UserID: owner, Name: "laptop", Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
		n, err := a.RevokeEphemeral(ctx, owner)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		_, err = a.Introspect(ctx, eph.Plaintext)
		require.ErrorIs(t, err, auth.ErrAccessTokenInactive)
		_, err = a.Introspect(ctx, durable.Plaintext)
		require.NoError(t, err)
	})

	t.Run("rotate replaces the same name and scope only", func(t *testing.T) {
		owner := user + "-rot"
		first := mint(t, MintRequest{UserID: owner, Name: "laptop", Scopes: []fat.Scope{fat.ScopeDaemonConnect}, Rotate: true})
		require.False(t, first.Rotated)
		other := mint(t, MintRequest{UserID: owner, Name: "desktop", Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
		second := mint(t, MintRequest{UserID: owner, Name: "laptop", Scopes: []fat.Scope{fat.ScopeDaemonConnect}, Rotate: true})
		require.True(t, second.Rotated)

		_, err := a.Introspect(ctx, first.Plaintext)
		require.ErrorIs(t, err, auth.ErrAccessTokenInactive, "rotated-out token must be dead")
		_, err = a.Introspect(ctx, second.Plaintext)
		require.NoError(t, err)
		_, err = a.Introspect(ctx, other.Plaintext)
		require.NoError(t, err, "a different name must survive rotation")
	})
}
