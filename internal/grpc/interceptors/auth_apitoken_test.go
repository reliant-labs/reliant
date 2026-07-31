// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/pat"
	"github.com/reliant-labs/reliant/internal/patauth"
)

// memPATStore is a minimal in-memory pat.Store.
type memPATStore struct {
	tokens map[string]*db.DaemonPAT // by ID
}

func newMemPATStore() *memPATStore {
	return &memPATStore{tokens: map[string]*db.DaemonPAT{}}
}

func (m *memPATStore) CreateDaemonPAT(_ context.Context, p *db.DaemonPAT) error {
	cp := *p
	m.tokens[p.ID] = &cp
	return nil
}

func (m *memPATStore) GetDaemonPATByTokenHash(_ context.Context, hash string) (*db.DaemonPAT, error) {
	for _, t := range m.tokens {
		if t.TokenHash == hash {
			cp := *t
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memPATStore) ListDaemonPATsByUserIDAndKind(_ context.Context, userID, kind string) ([]*db.DaemonPAT, error) {
	var out []*db.DaemonPAT
	for _, t := range m.tokens {
		if t.UserID == userID && t.Kind == kind {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memPATStore) RevokeDaemonPATByUserID(_ context.Context, userID, id, kind string) (bool, error) {
	t, ok := m.tokens[id]
	if !ok || t.UserID != userID || t.Kind != kind || t.RevokedAt != nil {
		return false, nil
	}
	now := time.Now().UTC()
	t.RevokedAt = &now
	return true, nil
}

func (m *memPATStore) RevokeDaemonPATsByUserID(_ context.Context, userID string, ephemeralOnly bool) error {
	now := time.Now().UTC()
	for _, t := range m.tokens {
		if t.UserID == userID && t.RevokedAt == nil && (!ephemeralOnly || t.Ephemeral) {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (m *memPATStore) RevokeDaemonPATsByDaemonID(_ context.Context, daemonID string) (int, error) {
	now := time.Now().UTC()
	n := 0
	for _, t := range m.tokens {
		if t.DaemonID == daemonID && t.RevokedAt == nil {
			t.RevokedAt = &now
			n++
		}
	}
	return n, nil
}

func (m *memPATStore) UpdateDaemonPATLastUsed(_ context.Context, id string) error {
	if t, ok := m.tokens[id]; ok {
		now := time.Now().UTC()
		t.LastUsedAt = &now
	}
	return nil
}

func bearerHeader(token string) func(string) string {
	return func(key string) string {
		if key == "Authorization" {
			return "Bearer " + token
		}
		return ""
	}
}

// TestAuthInterceptorPATDispatch proves the full prefix-dispatch slice at the
// interceptor: rlnt_pat_ bearers resolve through the unified PAT service to
// the same claims/identity object JWT (here: apikey-mode) auth produces —
// api-kind ONLY. Daemon-kind tokens, revoked tokens, and unknown tokens are
// rejected, and non-PAT bearers behave as before.
func TestAuthInterceptorPATDispatch(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	store := newMemPATStore()
	svc := pat.NewService(store)

	interceptor, err := NewAuthInterceptor("", "", []string{"/reliant.v1.SystemService/Health"})
	require.NoError(t, err)
	interceptor.SetAPITokenValidator(svc)

	ctx := context.Background()

	rawAPIToken, created, err := svc.CreateAPIToken(ctx, "user-pat", "pat@example.com", "interceptor-test", 0)
	require.NoError(t, err)
	rawDaemonToken, _, err := svc.CreatePAT(ctx, "user-pat", "daemon-cred", false, nil)
	require.NoError(t, err)

	t.Run("valid api-kind PAT resolves identity", func(t *testing.T) {
		authedCtx, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(rawAPIToken))
		require.NoError(t, err)
		require.Equal(t, "user-pat", claims.Sub)
		require.Equal(t, "pat@example.com", claims.Email)

		userID, ok := auth.GetUserIDFromContext(authedCtx)
		require.True(t, ok)
		require.Equal(t, "user-pat", userID)
		email, ok := auth.GetUserEmailFromContext(authedCtx)
		require.True(t, ok)
		require.Equal(t, "pat@example.com", email)
	})

	t.Run("daemon-kind PAT is rejected (kind separation)", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(rawDaemonToken))
		require.Error(t, err, "a daemon token must never authenticate user APIs")
	})

	t.Run("PAT is never stored as a user JWT", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(rawAPIToken))
		require.NoError(t, err)
		if jwt, ok := auth.GetUserJWT("user-pat"); ok {
			require.NotEqual(t, rawAPIToken, jwt, "PAT leaked into the user-JWT store")
		}
	})

	t.Run("PAT denied on token-management procedures", func(t *testing.T) {
		// A PAT cannot mint/list/revoke PATs — DaemonTokenService requires a
		// session credential.
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.DaemonTokenService/CreateDaemonToken", bearerHeader(rawAPIToken))
		require.Error(t, err)
		// The session (apikey-mode) credential still may.
		_, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.DaemonTokenService/CreateDaemonToken", bearerHeader("legacy-api-key-secret"))
		require.NoError(t, err)
		require.NotEmpty(t, claims.Sub)
	})

	t.Run("legacy bearer path still works", func(t *testing.T) {
		authedCtx, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader("legacy-api-key-secret"))
		require.NoError(t, err)
		require.NotEmpty(t, claims.Sub)
		_, ok := auth.GetUserIDFromContext(authedCtx)
		require.True(t, ok)
	})

	t.Run("unknown PAT rejected", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader("rlnt_pat_000000000000000000000000000000"))
		require.Error(t, err)
	})

	t.Run("revoked PAT rejected", func(t *testing.T) {
		require.NoError(t, svc.RevokeAPIToken(ctx, "user-pat", created.ID))
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(rawAPIToken))
		require.Error(t, err)
	})

	t.Run("public methods skip auth entirely", func(t *testing.T) {
		_, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.SystemService/Health", func(string) string { return "" })
		require.NoError(t, err)
		require.Nil(t, claims)
	})
}

// TestPatauthRejectsAPIKindToken is the other half of the kind separation,
// proven at the gateway-side validator (internal/patauth): an api-kind token
// must never authenticate a daemon <-> gateway stream, while a daemon-kind
// token still does.
func TestPatauthRejectsAPIKindToken(t *testing.T) {
	store := newMemPATStore()
	svc := pat.NewService(store)
	ctx := context.Background()

	rawAPIToken, _, err := svc.CreateAPIToken(ctx, "user-1", "u1@example.com", "api-cred", 0)
	require.NoError(t, err)
	rawDaemonToken, daemonPAT, err := svc.CreatePAT(ctx, "user-1", "daemon-cred", false, nil)
	require.NoError(t, err)

	validator := patauth.NewDBPATValidator(store)

	_, _, _, err = validator.ValidatePAT(ctx, rawAPIToken)
	require.Error(t, err, "an api token must never authenticate the daemon path")

	userID, patID, daemonID, err := validator.ValidatePAT(ctx, rawDaemonToken)
	require.NoError(t, err)
	require.Equal(t, "user-1", userID)
	require.Equal(t, daemonPAT.ID, patID)
	require.Empty(t, daemonID)
}

// TestAuthInterceptorPATDisabled verifies rlnt_pat_ bearers are rejected
// outright when no PAT validator is wired (e.g. a server built without the
// DB), instead of falling through to JWT validation.
func TestAuthInterceptorPATDisabled(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	interceptor, err := NewAuthInterceptor("", "", nil)
	require.NoError(t, err)

	_, _, _, err = interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/GetChat", bearerHeader("rlnt_pat_000000000000000000000000000000"))
	require.Error(t, err)
}

// unreachablePATStore is a pat.Store whose hash lookup fails the way a real one
// does when Postgres is unreachable: not "no rows", an actual error.
type unreachablePATStore struct{ *memPATStore }

func (unreachablePATStore) GetDaemonPATByTokenHash(_ context.Context, _ string) (*db.DaemonPAT, error) {
	return nil, fmt.Errorf("failed to connect to `user=postgres database=reliant`: %w", context.DeadlineExceeded)
}

// TestAuthInterceptorUnreachableTokenStoreIsNotARejection pins the distinction
// the CLI's error text depends on.
//
// Observed for real: a Postgres stall made every PAT lookup fail, the
// interceptor mapped it to CodeUnauthenticated, and the CLI told the operator
// "the API token stored in context "default" was rejected ... run
// 'reliant auth token create'" — a confident instruction that cannot fix an
// unreachable database and hides the outage.
//
// Both directions are asserted. A test that only checked the outage case would
// pass if every error became Unavailable, which would break real rejections.
func TestAuthInterceptorUnreachableTokenStoreIsNotARejection(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	ctx := context.Background()
	good := pat.NewService(newMemPATStore())
	rawToken, _, err := good.CreateAPIToken(ctx, "user-pat", "pat@example.com", "unavailable-test", 0)
	require.NoError(t, err)

	t.Run("store unreachable is Unavailable, not Unauthenticated", func(t *testing.T) {
		interceptor, err := NewAuthInterceptor("", "", nil)
		require.NoError(t, err)
		interceptor.SetAPITokenValidator(pat.NewService(unreachablePATStore{newMemPATStore()}))

		_, _, _, err = interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(rawToken))
		require.Error(t, err)
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
			"a token store that could not be reached has not rejected anything")
	})

	t.Run("genuinely unknown token is still Unauthenticated", func(t *testing.T) {
		interceptor, err := NewAuthInterceptor("", "", nil)
		require.NoError(t, err)
		interceptor.SetAPITokenValidator(good)

		_, _, _, err = interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat",
			bearerHeader("rlnt_pat_000000000000000000000000000000"))
		require.Error(t, err)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}
