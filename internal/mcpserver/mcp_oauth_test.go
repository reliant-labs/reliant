// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// An MCP client that authorized at the control plane (cliauth as the OAuth
// authorization server) presents an `rlat_` holding mcp:oauth, issued for one
// RFC 8707 resource. These tests pin when the workspace connector accepts one
// (the control plane is a listed authorization server, and the audience is
// exactly this endpoint) and that no other `rlat_` rides the same path.

const (
	mcpOAuthIssuer   = "https://cp.example.com"
	mcpOAuthResource = "https://api.example.com/mcp"
	mcpOAuthClient   = "mcp_abc"
)

// mcpOAuthConfig is a deployment that lists the control plane second, behind
// Supabase, as before cutover: its tokens are accepted, and Supabase stays the
// server clients use first. The trailing slash is deliberate.
func mcpOAuthConfig() OAuthConfig {
	return OAuthConfig{
		PublicURL:            "https://api.example.com",
		AuthorizationServers: []string{"https://project.supabase.co", mcpOAuthIssuer + "/"},
		AccessTokenIssuer:    mcpOAuthIssuer,
	}
}

// rlatToken is a well-formed `rlat_` (fat.HasFormat) distinguished by n.
func rlatToken(n int) string {
	s := fmt.Sprint(n)
	return fat.Prefix + strings.Repeat("0", fat.TokenLen-len(fat.Prefix)-len(s)) + s
}

// principalIntrospector answers for the principals it holds, and defers to
// inner (the memStore's token authority) for any other token.
type principalIntrospector struct {
	principals map[string]*fat.Principal
	inner      CredentialIntrospector
}

func (p principalIntrospector) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	if pr, ok := p.principals[token]; ok {
		return pr, nil
	}
	if p.inner != nil {
		return p.inner.Introspect(ctx, token)
	}
	return nil, errors.New("inactive")
}

// mcpOAuthPrincipal is a valid MCP client token for this endpoint.
func mcpOAuthPrincipal() *fat.Principal {
	return &fat.Principal{
		TokenID:      "tok-mcp",
		OrgID:        "org-1",
		ActingUserID: "user-1",
		Scopes:       fat.SetOf(fat.ScopeMCPOAuth),
		ClientID:     mcpOAuthClient,
		Audience:     mcpOAuthResource,
	}
}

// observed is what reached the endpoint behind the authenticator.
type observed struct {
	session     *Session
	callerToken string
}

// serveMCPOAuth runs one request with token through the workspace
// authenticator and returns the response plus what the endpoint saw.
func serveMCPOAuth(t *testing.T, store *memStore, cfg OAuthConfig, principals map[string]*fat.Principal,
	jwt OAuthTokenValidator, token string,
) (*httptest.ResponseRecorder, *observed) {
	t.Helper()
	a := &connectorAuth{
		store:       store,
		credentials: principalIntrospector{principals: principals, inner: store.creds},
		oauth:       cfg,
		tokens:      jwt,
		bindings:    store,
		consentBase: "https://app.example.com",
		logger:      slog.Default(),
	}
	var seen *observed
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = &observed{session: sessionFrom(r.Context()), callerToken: CallerToken(r.Context())}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, MountPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.middleware(inner).ServeHTTP(rec, req)
	return rec, seen
}

// requireRefused asserts the standard 401 challenge, with nothing reaching
// the endpoint.
func requireRefused(t *testing.T, rec *httptest.ResponseRecorder, seen *observed, why string) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, rec.Code, why)
	require.Nil(t, seen, "%s: the request reached the endpoint", why)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="reliant-workspace"`)
}

// The token resolves exactly as an OAuth JWT does: by the user's connectors,
// and it is forwarded as the caller's token for a workspace resume.
func TestMCPOAuthTokenWithItsExactAudienceIsAccepted(t *testing.T) {
	store := newMemStore()
	g := addGrant(t, store, "user-1", "phone")
	token := rlatToken(1)

	rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), map[string]*fat.Principal{token: mcpOAuthPrincipal()}, nil, token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, seen)
	require.Equal(t, g.ID, seen.session.GrantID)
	require.Equal(t, "user-1", seen.session.UserID)
	require.Equal(t, token, seen.callerToken, "an mcp:oauth token is the caller's token for ResumeDaemon")
}

// Consent is per OAuth client, as for a JWT: the binding the user recorded
// for this client decides, and another client's binding never does.
func TestMCPOAuthTokenUsesItsClientsConsent(t *testing.T) {
	store := newMemStore()
	addGrant(t, store, "user-1", "laptop")
	chosen := addGrant(t, store, "user-1", "phone")
	token := rlatToken(2)
	principals := map[string]*fat.Principal{token: mcpOAuthPrincipal()}

	// Ambiguous and unbound: consent, naming this client.
	rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), principals, nil, token)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Nil(t, seen)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Contains(t, body["consent_url"], ConsentPath)
	require.Contains(t, body["consent_url"], mcpOAuthClient)

	// Another client's binding does not apply.
	require.NoError(t, store.PutBinding(context.Background(), &connectorgrant.ClientBinding{
		ID: "b-other", UserID: "user-1",
		ClientID: connectorgrant.BindingClientID(connectorgrant.KindWorkspace, "chatgpt"), GrantID: chosen.ID,
	}))
	rec, _ = serveMCPOAuth(t, store, mcpOAuthConfig(), principals, nil, token)
	require.Equal(t, http.StatusForbidden, rec.Code, "a different client's consent must not authorize this one")

	// This client's binding does.
	require.NoError(t, store.PutBinding(context.Background(), &connectorgrant.ClientBinding{
		ID: "b-mcp", UserID: "user-1",
		ClientID: connectorgrant.BindingClientID(connectorgrant.KindWorkspace, mcpOAuthClient), GrantID: chosen.ID,
	}))
	rec, seen = serveMCPOAuth(t, store, mcpOAuthConfig(), principals, nil, token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, chosen.ID, seen.session.GrantID)
}

// The audience binds the token to this endpoint and nothing else: one issued
// for the forge connector, another host, a near-miss spelling or no resource
// at all is refused, never narrowed.
func TestMCPOAuthTokenForAnotherAudienceIsRefused(t *testing.T) {
	for name, aud := range map[string]string{
		"forge connector": "https://api.example.com/forge/mcp",
		"other host":      "https://evil.example.com/mcp",
		"trailing slash":  mcpOAuthResource + "/",
		"other scheme":    "http://api.example.com/mcp",
		"origin only":     "https://api.example.com",
		"empty":           "",
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemStore()
			addGrant(t, store, "user-1", "phone")
			p := mcpOAuthPrincipal()
			p.Audience = aud
			token := rlatToken(3)

			rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), map[string]*fat.Principal{token: p}, nil, token)
			requireRefused(t, rec, seen, "audience "+aud)
		})
	}
}

// Acceptance is off unless the control plane is one of the advertised
// authorization servers: a deployment that names only Supabase, or names no
// control plane, or advertises nothing, refuses a perfectly valid token.
func TestMCPOAuthTokenIsRefusedWhileAcceptanceIsOff(t *testing.T) {
	for name, mutate := range map[string]func(*OAuthConfig){
		"control plane not listed": func(c *OAuthConfig) { c.AuthorizationServers = []string{"https://project.supabase.co"} },
		"no control plane":         func(c *OAuthConfig) { c.AccessTokenIssuer = "" },
		"another control plane":    func(c *OAuthConfig) { c.AccessTokenIssuer = "https://other-cp.example.com" },
		"no public URL":            func(c *OAuthConfig) { c.PublicURL = "" },
		"no authorization servers": func(c *OAuthConfig) { c.AuthorizationServers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemStore()
			addGrant(t, store, "user-1", "phone")
			cfg := mcpOAuthConfig()
			mutate(&cfg)
			token := rlatToken(4)

			rec, seen := serveMCPOAuth(t, store, cfg, map[string]*fat.Principal{token: mcpOAuthPrincipal()}, nil, token)
			requireRefused(t, rec, seen, name)
		})
	}
}

// No other access token rides the MCP client path, even when it carries a
// client and this endpoint's audience: a CLI token, a daemon's wildcard, an
// org automation or operator token with no acting user, an mcp:oauth token
// widened by another scope, and one bound to a resource.
func TestOtherAccessTokensAreRefusedAtTheWorkspaceConnector(t *testing.T) {
	withClient := func(p *fat.Principal) *fat.Principal {
		p.ClientID, p.Audience = mcpOAuthClient, mcpOAuthResource
		return p
	}
	for name, p := range map[string]*fat.Principal{
		"reliant:api CLI token": withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeReliantAPI)}),
		"daemon wildcard": withClient(&fat.Principal{
			ActingUserID: "user-1", AllScopes: true, Scopes: fat.SetOf(fat.ScopeMCPOAuth, fat.ScopeDaemonConnect),
			Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"},
		}),
		"daemon wildcard, unbound": withClient(&fat.Principal{
			ActingUserID: "user-1", AllScopes: true, Scopes: fat.SetOf(fat.ScopeMCPOAuth),
		}),
		"no acting user (org automation or operator)": withClient(&fat.Principal{OrgID: "org-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth)}),
		"mcp:oauth with another scope":                withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth, fat.ScopeReliantAPI)}),
		"mcp:oauth with no client":                    {ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth), Audience: mcpOAuthResource},
		"mcp:oauth bound to a connector": withClient(&fat.Principal{
			ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth),
			Resource: &fat.Resource{Kind: fat.ResourceConnector, ID: "phone"},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemStore()
			addGrant(t, store, "user-1", "phone")
			token := rlatToken(5)

			rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), map[string]*fat.Principal{token: p}, nil, token)
			requireRefused(t, rec, seen, name)
		})
	}
}

// A JWT the deployment's validator refuses (a managed-identity operator
// token, signed by another issuer) is refused whatever MCP_OAUTH_ISSUERS
// lists: listing the control plane accepts its `rlat_` tokens, not its JWTs.
// The real validator's refusal of such a token is pinned in internal/grpc
// (TestConnectorValidatorRefusesAManagedIdentityOperatorJWT).
func TestOperatorJWTIsRefusedWithAcceptanceOn(t *testing.T) {
	store := newMemStore()
	addGrant(t, store, "user-1", "phone")

	rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), nil,
		&stubTokenValidator{err: errors.New("token is signed by an unknown issuer")}, "eyJhbGciOiJSUzI1NiJ9.operator.sig")
	requireRefused(t, rec, seen, "operator JWT")
}

// Turning acceptance on changes neither existing credential: a connector
// credential still resolves to its grant and is still NOT forwarded as the
// caller, and a Supabase OAuth JWT still resolves and is forwarded.
func TestExistingCredentialsAreUnchangedWithAcceptanceOn(t *testing.T) {
	t.Run("connector credential", func(t *testing.T) {
		store := newMemStore()
		g := addGrant(t, store, "user-1", "phone")
		raw := store.credentialFor(t, g.ID, "user-1")

		rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), nil, nil, raw)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, g.ID, seen.session.GrantID)
		require.Empty(t, seen.callerToken, "a connector credential is never forwarded as the caller")
	})
	t.Run("supabase OAuth JWT", func(t *testing.T) {
		store := newMemStore()
		g := addGrant(t, store, "user-1", "phone")

		rec, seen := serveMCPOAuth(t, store, mcpOAuthConfig(), nil,
			&stubClientTokenValidator{userID: "user-1", clientID: "chatgpt"}, "a-supabase-jwt")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, g.ID, seen.session.GrantID)
		require.Equal(t, "a-supabase-jwt", seen.callerToken)
	})
}

// The RFC 9728 document advertises the authorization servers in the order
// configured (clients use the first) and the configured scopes, on both
// connectors.
func TestDiscoveryAdvertisesTheConfiguredServersAndScopes(t *testing.T) {
	cfg := mcpOAuthConfig()
	cfg.Scopes = []string{"openid", "mcp:oauth"}

	for name, h := range map[string]http.Handler{
		"workspace": NewProtectedResourceMetadataHandler(cfg),
		"forge":     newProtectedResourceMetadataHandler(cfg, forgeResource),
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			resp, err := http.Get(srv.URL)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			var doc protectedResourceMetadata
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
			require.Equal(t, []string{"https://project.supabase.co", mcpOAuthIssuer + "/"}, doc.AuthorizationServers)
			require.Equal(t, []string{"openid", "mcp:oauth"}, doc.ScopesSupported)
		})
	}
	require.Contains(t, cfg.challengeHeader(), `scope="openid mcp:oauth"`)
}

// The forge connector refuses every `rlat_` in this release, an mcp:oauth
// token issued for it included: the forge tools act at the control plane
// with authority such a token does not carry (follow-up R9b).
func TestForgeConnectorRefusesMCPOAuthTokens(t *testing.T) {
	store := newMemStore()
	h, err := NewForgeHTTPHandler(ForgeHTTPDeps{
		Store:           store,
		TokenValidator:  &stubClientTokenValidator{userID: "user-1", clientID: mcpOAuthClient},
		Bindings:        store,
		OAuth:           mcpOAuthConfig(),
		ControlPlaneURL: "https://cp.internal",
		ConsentBaseURL:  "https://app.example.com",
		Logger:          slog.Default(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, ForgeMountPath, nil)
	req.Header.Set("Authorization", "Bearer "+rlatToken(6))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="reliant-forge"`)
}
