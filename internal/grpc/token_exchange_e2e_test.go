// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/handlers/token"
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
	// The member holds the whole deploy family, but no db:query grant.
	cp := &fakeControlPlane{t: t, secret: secret, store: tokenauthority.NewMemory(), grants: map[string]fat.Set{
		userID: fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite,
			fat.ScopeDomainRead, fat.ScopeDomainWrite),
	}}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)

	authority, mode, err := tokenauthority.New(tokenauthority.DepsFor(controlplane.Endpoint{URL: cpSrv.URL, InternalServiceSecret: secret}, nil))
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, token.TokenControlPlane{
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
	for _, derived := range []fat.Scope{fat.ScopeObsRead, fat.ScopeObsTriage, fat.ScopeDBQuery} {
		require.False(t, parent.Scopes.Has(derived), "a long-lived daemon credential never carries %s: %s", derived, parent.Scopes)
	}
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
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "wrong audience: %v", err)
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
	require.ElementsMatch(t, []string{"deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write"}, exchanged.Msg.GetScopes())
	child, err := cp.store.Introspect(ctx, forgeToken)
	require.NoError(t, err)
	require.Equal(t, userID, child.ActingUserID)
	require.Equal(t, parent.OrgID, child.OrgID)
	require.Equal(t, fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite,
		fat.ScopeDomainRead, fat.ScopeDomainWrite), child.Scopes)
	require.NotNil(t, child.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Hour), *child.ExpiresAt, time.Minute, "an exchanged token lives an hour")
	expiresAt, err := time.Parse(time.RFC3339, exchanged.Msg.GetExpiresAt())
	require.NoError(t, err)
	require.WithinDuration(t, *child.ExpiresAt, expiresAt, time.Second)

	// 4. It carries no session authority: not an API credential, not a daemon.
	_, err = tokens(forgeToken).ListTokens(ctx, connect.NewRequest(&reliantv1.ListTokensRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "an exchanged token must not call reliant's API: %v", err)
	_, err = connectDaemon(t, gatewayURL, forgeToken)
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "an exchanged token must not connect a daemon: %v", err)
	// …and it cannot be exchanged again: it is not a session.
	_, err = tokens(forgeToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "re-exchange: %v", err)

	// 5. Revoking the parent is honoured on the very next exchange — the
	//    handler introspects uncached.
	_, err = tokens(session).RevokeToken(ctx, connect.NewRequest(&reliantv1.RevokeTokenRequest{Id: created.Msg.GetInfo().GetId()}))
	require.NoError(t, err)
	_, err = tokens(daemonToken).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "a revoked parent: %v", err)
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
	apiURL := serveTokenService(t, authority, signer.pubPEM, token.TokenControlPlane{Issuer: controlPlane})
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
			require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "escalation: %v", err)
			require.Contains(t, err.Error(), "reliant auth login")
			after, err := authority.ListForUser(ctx, userID, "")
			require.NoError(t, err)
			require.Len(t, after, len(before), "a refused exchange must not mint")
		})
	}

	// A human session has no credential to attenuate.
	_, err := tokens(session).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: controlPlane}))
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "a session JWT: %v", err)
	require.True(t, strings.Contains(err.Error(), "rlat_"), "the refusal names what it needs: %v", err)
}

// TestForgeCredential_DerivedScopesEndToEnd: obs:read reaches a member's daemon
// credential through the exchange although the credential never held it, and
// db:query only with the org grant — clipped by control-plane's UpdateForUser,
// which this fake models.
func TestForgeCredential_DerivedScopesEndToEnd(t *testing.T) {
	const (
		secret       = "e2e-internal-service-secret"
		controlPlane = "https://admin.example.com"
	)
	cp := &fakeControlPlane{t: t, secret: secret, store: tokenauthority.NewMemory(), grants: map[string]fat.Set{
		"with-grant":    fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeDBQuery),
		"without-grant": fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite),
	}}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFor(controlplane.Endpoint{URL: cpSrv.URL, InternalServiceSecret: secret}, nil))
	require.NoError(t, err)
	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, token.TokenControlPlane{
		Issuer: controlPlane, ClipsGrants: mode == tokenauthority.ModeControlPlane,
	})
	tokens := func(bearer string) reliantv1connect.TokenServiceClient {
		return reliantv1connect.NewTokenServiceClient(&http.Client{Transport: bearerTransport(bearer)}, apiURL)
	}
	ctx := context.Background()

	for user, want := range map[string][]string{
		"with-grant":    {"obs:read", "db:query"},
		"without-grant": {"obs:read"},
	} {
		created, err := tokens(signer.sign(t, user)).CreateToken(ctx, connect.NewRequest(&reliantv1.CreateTokenRequest{
			Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
		}))
		require.NoError(t, err)
		exchanged, err := tokens(created.Msg.GetToken()).ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{
			Audience: controlPlane, Scopes: []string{"obs:read", "db:query"},
		}))
		require.NoError(t, err, user)
		require.ElementsMatch(t, want, exchanged.Msg.GetScopes(), user)
		child, err := cp.store.Introspect(ctx, exchanged.Msg.GetToken())
		require.NoError(t, err)
		require.Equal(t, fat.SetOf(scopesOf(want)...), child.Scopes, user)
		require.Equal(t, user, child.ActingUserID)
	}
}

func scopesOf(raw []string) []fat.Scope {
	out := make([]fat.Scope, len(raw))
	for i, s := range raw {
		out[i] = fat.Scope(s)
	}
	return out
}
