// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// ── End-to-end: a Reliant session becomes forge's control-plane credential ──
//
// TestForgeCredential_HostedEndToEnd drives the whole hosted chain with only
// control-plane's STORAGE faked (fakeControlPlane, Memory under real grant
// rules):
//
//	app session ─► TokenService.CreateToken(DAEMON) ─► MintForUser + clipped UpdateForUser
//	daemon credential ─► TokenService.ExchangeToken (public at the interceptor,
//	                     authenticated in the handler) ─► MintForUser ─► `rlat_`
//
// and pins every property the exchange promises: attenuation to what the
// daemon credential holds (which is the user's grants, clipped at mint), no
// session authority on the result, the audience check before any mint, and
// revocation of the parent honoured immediately.
func TestForgeCredential_HostedEndToEnd(t *testing.T) {
	const (
		secret       = "e2e-internal-service-secret"
		userID       = "user-e2e-forge"
		controlPlane = "https://admin.example.com"
	)
	// The member holds deploy and secret authority, but no domain grant.
	cp := &fakeControlPlane{t: t, secret: secret, store: tokenauthority.NewMemory(), grants: map[string]fat.Set{
		userID: fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite),
	}}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)

	t.Setenv("RELIANT_CONTROL_PLANE_URL", cpSrv.URL)
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")
	t.Setenv("INTERNAL_SERVICE_SECRET", secret)
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFromEnv(nil))
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, services.TokenControlPlane{
		Issuer: controlPlane, ClipsGrants: mode == tokenauthority.ModeControlPlane,
	})
	gatewayURL := serveGateway(t, authority)
	tokens := func(bearer string) reliantv1connect.TokenServiceClient {
		return reliantv1connect.NewTokenServiceClient(&http.Client{Transport: bearerTransport(bearer)}, apiURL)
	}
	session := signer.sign(t, userID)
	ctx := context.Background()

	// 1. The app mints its daemon credential with a session. Its ceiling is
	//    clipped to the member's grants: deploy + secret, not domain.
	created, err := tokens(session).CreateToken(ctx, connect.NewRequest(&reliantv1.CreateTokenRequest{
		Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	require.NoError(t, err)
	daemonToken := created.Msg.GetToken()
	parent, err := cp.store.Introspect(ctx, daemonToken)
	require.NoError(t, err)
	require.True(t, parent.Scopes.Has(fat.ScopeDaemonConnect))
	require.True(t, parent.Scopes.Has(fat.ScopeDeployWrite), "the daemon credential carries its user's deploy authority: %s", parent.Scopes)
	require.False(t, parent.Scopes.Has(fat.ScopeDomainWrite), "a grant the member lacks must be clipped: %s", parent.Scopes)
	require.False(t, parent.Scopes.Has(fat.ScopeReliantAPI), "a daemon credential stays a daemon's, not an API credential")
	require.Equal(t, reliantv1.TokenKind_TOKEN_KIND_DAEMON, created.Msg.GetInfo().GetKind())
	msg, err := connectDaemon(t, gatewayURL, daemonToken)
	require.NoError(t, err)
	require.True(t, msg.GetRegistrationAck().GetAccepted(), "the widened credential still connects its daemon")

	// 2. A wrong audience mints NOTHING. A repo's KCL names the endpoint.
	before, err := cp.store.ListForUser(ctx, userID, "")
	require.NoError(t, err)
	_, err = tokens(daemonToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{
		Audience: "https://evil.example.com",
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "wrong audience: %v", err)
	after, err := cp.store.ListForUser(ctx, userID, "")
	require.NoError(t, err)
	require.Len(t, after, len(before), "a refused audience must not mint")

	// 3. The daemon credential is exchanged for forge's credential.
	exchanged, err := tokens(daemonToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{
		Audience: controlPlane + "/",
		Scopes:   []string{"deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write"},
	}))
	require.NoError(t, err)
	forgeToken := exchanged.Msg.GetToken()
	require.True(t, fat.HasFormat(forgeToken))
	require.NotEqual(t, daemonToken, forgeToken)
	require.ElementsMatch(t, []string{"deploy:read", "deploy:write", "secret:read", "secret:write"}, exchanged.Msg.GetScopes(),
		"granted = requested ∩ held: the domain scopes the daemon credential lacks are not granted")
	child, err := cp.store.Introspect(ctx, forgeToken)
	require.NoError(t, err)
	require.Equal(t, userID, child.ActingUserID)
	require.Equal(t, parent.OrgID, child.OrgID)
	require.Equal(t, fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite), child.Scopes)
	require.NotNil(t, child.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Hour), *child.ExpiresAt, time.Minute, "an exchanged token lives an hour")
	expiresAt, err := time.Parse(time.RFC3339, exchanged.Msg.GetExpiresAt())
	require.NoError(t, err)
	require.WithinDuration(t, *child.ExpiresAt, expiresAt, time.Second)

	// 4. It carries no session authority: not an API credential, not a daemon.
	_, err = tokens(forgeToken).ListTokens(ctx, connect.NewRequest(&reliantv1.ListTokensRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "an exchanged token must not call reliant's API: %v", err)
	_, err = connectDaemon(t, gatewayURL, forgeToken)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "an exchanged token must not connect a daemon: %v", err)
	// …and it cannot be exchanged again: it is not a session.
	_, err = tokens(forgeToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "re-exchange: %v", err)

	// 5. Revoking the parent is honoured on the very next exchange — the
	//    handler introspects uncached.
	_, err = tokens(session).RevokeToken(ctx, connect.NewRequest(&reliantv1.RevokeTokenRequest{Id: created.Msg.GetInfo().GetId()}))
	require.NoError(t, err)
	_, err = tokens(daemonToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "a revoked parent: %v", err)
	require.Contains(t, err.Error(), "sign in to Reliant again")
}

// TestForgeCredential_NeverEscalates: a session credential that does not hold
// deploy authority — a CLI login with reliant:api only, or a daemon credential
// minted where nothing clipped a ceiling onto it — gets nothing, and is told
// how to get a credential that has it. Exchanging it for deploy:write would be
// a token granting authority it does not hold.
func TestForgeCredential_NeverEscalates(t *testing.T) {
	const (
		userID       = "user-e2e-narrow"
		controlPlane = "https://admin.example.com"
	)
	authority := tokenauthority.NewMemory()
	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, services.TokenControlPlane{Issuer: controlPlane})
	tokens := func(bearer string) reliantv1connect.TokenServiceClient {
		return reliantv1connect.NewTokenServiceClient(&http.Client{Transport: bearerTransport(bearer)}, apiURL)
	}
	session := signer.sign(t, userID)
	ctx := context.Background()

	for _, kind := range []reliantv1.TokenKind{reliantv1.TokenKind_TOKEN_KIND_API, reliantv1.TokenKind_TOKEN_KIND_DAEMON} {
		t.Run(kind.String(), func(t *testing.T) {
			created, err := tokens(session).CreateToken(ctx, connect.NewRequest(&reliantv1.CreateTokenRequest{
				Name: "narrow-" + kind.String(), Kind: kind,
			}))
			require.NoError(t, err)
			before, err := authority.ListForUser(ctx, userID, "")
			require.NoError(t, err)

			_, err = tokens(created.Msg.GetToken()).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{
				Audience: controlPlane,
			}))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "escalation: %v", err)
			require.Contains(t, err.Error(), "reliant auth login")
			after, err := authority.ListForUser(ctx, userID, "")
			require.NoError(t, err)
			require.Len(t, after, len(before), "a refused exchange must not mint")
		})
	}

	// A human session has no credential to attenuate.
	_, err := tokens(session).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "a session JWT: %v", err)
	require.True(t, strings.Contains(err.Error(), "rlat_"), "the refusal names what it needs: %v", err)
}
