// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

func bearerHeader(token string) func(string) string {
	return func(key string) string {
		if key == "Authorization" {
			return "Bearer " + token
		}
		return ""
	}
}

func mintFor(t *testing.T, authority *tokenauthority.Memory, userID string, scopes ...fat.Scope) tokenauthority.Minted {
	t.Helper()
	m, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: userID, Name: "interceptor-test", Scopes: scopes,
	})
	require.NoError(t, err)
	return m
}

// TestAuthInterceptorAccessTokenDispatch proves the shape dispatch at the
// interceptor: `rlat_` bearers resolve through the token authority to the same
// identity a session produces — reliant:api ONLY. Daemon credentials, revoked
// and unknown tokens are rejected, and non-`rlat_` bearers behave as before.
func TestAuthInterceptorAccessTokenDispatch(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	authority := tokenauthority.NewMemory()
	interceptor, err := NewAuthInterceptor("", "", []string{"/reliant.v1.SystemService/Health"})
	require.NoError(t, err)
	interceptor.SetAccessTokenIntrospector(authority)

	ctx := context.Background()
	apiToken := mintFor(t, authority, "user-at", fat.ScopeReliantAPI)
	daemonToken := mintFor(t, authority, "user-at", fat.ScopeDaemonConnect)

	t.Run("reliant:api token resolves identity and marks the context machine", func(t *testing.T) {
		authedCtx, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(apiToken.Plaintext))
		require.NoError(t, err)
		require.Equal(t, "user-at", claims.Sub)
		userID, ok := auth.GetUserIDFromContext(authedCtx)
		require.True(t, ok)
		require.Equal(t, "user-at", userID)
		p, ok := auth.MachineTokenFromContext(authedCtx)
		require.True(t, ok)
		require.Equal(t, apiToken.TokenID, p.TokenID)
	})

	t.Run("daemon:connect token is refused (scope separation)", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(daemonToken.Plaintext))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "a daemon token must never authenticate user APIs")
	})

	t.Run("access token is never stored as a user JWT", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(apiToken.Plaintext))
		require.NoError(t, err)
		if jwt, ok := auth.GetUserJWT("user-at"); ok {
			require.NotEqual(t, apiToken.Plaintext, jwt, "access token leaked into the user-JWT store")
		}
	})

	t.Run("session bearer path still works and is not machine", func(t *testing.T) {
		authedCtx, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader("legacy-api-key-secret"))
		require.NoError(t, err)
		require.NotEmpty(t, claims.Sub)
		_, isMachine := auth.MachineTokenFromContext(authedCtx)
		require.False(t, isMachine)
	})

	t.Run("unknown access token rejected", func(t *testing.T) {
		unknown, err := fat.Mint()
		require.NoError(t, err)
		_, _, _, err = interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(unknown.Plaintext))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("retired rlnt_pat_ is not an access token", func(t *testing.T) {
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader("rlnt_pat_000000000000000000000000000000"))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("revoked access token rejected", func(t *testing.T) {
		require.NoError(t, authority.RevokeForUser(ctx, "user-at", apiToken.TokenID))
		_, _, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.ChatService/GetChat", bearerHeader(apiToken.Plaintext))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("public methods skip auth entirely", func(t *testing.T) {
		_, claims, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.SystemService/Health", func(string) string { return "" })
		require.NoError(t, err)
		require.Nil(t, claims)
	})
}

// With no introspector wired, `rlat_` bearers are rejected outright instead of
// falling through to JWT validation.
func TestAuthInterceptorAccessTokensDisabled(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	interceptor, err := NewAuthInterceptor("", "", nil)
	require.NoError(t, err)
	token, err := fat.Mint()
	require.NoError(t, err)

	_, _, _, err = interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/GetChat", bearerHeader(token.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestAuthInterceptorUnreachableAuthorityIsNotARejection pins the distinction
// the CLI's error text depends on: an authority that could not be reached has
// rejected nothing, so the caller must not be told to mint a new token.
// Both directions are asserted — a test that only checked the outage would
// pass if every error became Unavailable.
func TestAuthInterceptorUnreachableAuthorityIsNotARejection(t *testing.T) {
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "legacy-api-key-secret")

	authority := tokenauthority.NewMemory()
	token := mintFor(t, authority, "user-at", fat.ScopeReliantAPI)
	interceptor, err := NewAuthInterceptor("", "", nil)
	require.NoError(t, err)
	interceptor.SetAccessTokenIntrospector(authority)

	tokenauthority.SetMemoryUnavailable(authority, true)
	_, _, _, err = interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/GetChat", bearerHeader(token.Plaintext))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
		"an authority that could not be reached has not rejected anything")

	tokenauthority.SetMemoryUnavailable(authority, false)
	unknown, err := fat.Mint()
	require.NoError(t, err)
	_, _, _, err = interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/GetChat", bearerHeader(unknown.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
