// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/stretchr/testify/require"
)

const (
	testInternalSecret = "internal-service-secret-with-at-least-32-bytes-of-entropy"
	gatedProcedure     = "/reliant.v1.DaemonTokenService/MintManagedDaemonToken"
	ungatedProcedure   = "/reliant.v1.DaemonTokenService/CreateDaemonToken"
)

// signInternalToken mirrors control-plane's SignInternalServiceToken so the RPC
// auth path is exercised against a token shaped exactly like the operator's.
func signInternalToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

func newTestInternalInterceptor(t *testing.T, secret string) *InternalServiceInterceptor {
	t.Helper()
	i, err := NewInternalServiceInterceptor(
		auth.NewInternalServiceVerifier(secret),
		[]string{gatedProcedure, "/reliant.v1.DaemonTokenService/RevokeManagedDaemonToken"},
	)
	require.NoError(t, err)
	return i
}

func hdr(token string) func(string) string {
	return func(key string) string {
		if key == "Authorization" && token != "" {
			return "Bearer " + token
		}
		return ""
	}
}

func TestInternalServiceInterceptor_RequiresVerifier(t *testing.T) {
	_, err := NewInternalServiceInterceptor(nil, nil)
	require.Error(t, err)
}

func TestInternalServiceInterceptor_AcceptsServiceToken(t *testing.T) {
	i := newTestInternalInterceptor(t, testInternalSecret)
	token := signInternalToken(t, testInternalSecret, jwt.MapClaims{
		"sub":  auth.InternalServiceSubject,
		"role": auth.InternalServiceRole,
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
	})

	ctx, err := i.authenticate(context.Background(), gatedProcedure, hdr(token))
	require.NoError(t, err)

	// Identity is injected as the internal-service principal.
	got, ok := auth.GetUserIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, auth.InternalServiceSubject, got)
}

func TestInternalServiceInterceptor_RejectsUserJWT(t *testing.T) {
	i := newTestInternalInterceptor(t, testInternalSecret)
	// A "user JWT" shape: same secret here would still fail because sub/role are
	// wrong; but the realistic case is an ES256 Supabase JWT that the HS256
	// verifier can't even validate. We model both: wrong-claims HS256 token...
	userish := signInternalToken(t, testInternalSecret, jwt.MapClaims{
		"sub":  "real-user-uuid",
		"role": "authenticated",
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := i.authenticate(context.Background(), gatedProcedure, hdr(userish))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// ...and an opaque/foreign token that isn't even a valid HS256 JWT.
	_, err = i.authenticate(context.Background(), gatedProcedure, hdr("eyJ-not-a-real-jwt"))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInternalServiceInterceptor_RejectsMissingHeader(t *testing.T) {
	i := newTestInternalInterceptor(t, testInternalSecret)
	_, err := i.authenticate(context.Background(), gatedProcedure, hdr(""))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInternalServiceInterceptor_FailsClosedWithoutSecret(t *testing.T) {
	// No secret configured: even a token signed with *some* secret is rejected.
	i := newTestInternalInterceptor(t, "")
	token := signInternalToken(t, "any-secret", jwt.MapClaims{
		"sub":  auth.InternalServiceSubject,
		"role": auth.InternalServiceRole,
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := i.authenticate(context.Background(), gatedProcedure, hdr(token))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInternalServiceInterceptor_PassesThroughUngatedProcedure(t *testing.T) {
	i := newTestInternalInterceptor(t, testInternalSecret)
	// For a non-gated procedure the interceptor is a no-op: it does NOT require
	// (or validate) an internal-service token, leaving auth to the user-JWT chain.
	ctx, err := i.authenticate(context.Background(), ungatedProcedure, hdr(""))
	require.NoError(t, err)
	// No internal-service identity was injected.
	_, ok := auth.GetUserIDFromContext(ctx)
	require.False(t, ok)
}
