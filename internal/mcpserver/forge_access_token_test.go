// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// An MCP client that authorized at the control plane for the Reliant Forge
// connector presents an `rlat_` holding mcp:forge, issued for this endpoint's
// RFC 8707 resource. These tests pin when /forge/mcp accepts one (the control
// plane is a listed authorization server, the audience is exactly this
// endpoint, and the token's organization is the grant's) and that no other
// `rlat_` rides the same path.

const mcpForgeResource = "https://api.example.com/forge/mcp"

// mcpForgePrincipal is a valid MCP client token for the forge connector,
// approved for org-1 (forgeGrant's organization).
func mcpForgePrincipal() *fat.Principal {
	return &fat.Principal{
		TokenID:      "tok-forge",
		OrgID:        "org-1",
		ActingUserID: "user-1",
		Scopes:       fat.SetOf(fat.ScopeMCPForge),
		ClientID:     mcpOAuthClient,
		Audience:     mcpForgeResource,
	}
}

// revocableIntrospector answers for the principals it holds; a revoked token
// (or any other) is inactive. Safe for a live server's goroutines.
type revocableIntrospector struct {
	mu         sync.Mutex
	principals map[string]*fat.Principal
}

func (r *revocableIntrospector) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.principals[token]; ok {
		return p, nil
	}
	return nil, errors.New("inactive")
}

func (r *revocableIntrospector) revoke(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.principals, token)
}

// forgeObserved is what reached the forge endpoint behind the authenticator.
type forgeObserved struct {
	session     *ForgeSession
	callerToken string
}

// newForgeAuth is the forge authenticator with cfg and an introspector
// answering for principals. jwt nil refuses every OAuth JWT, so an accepted
// request was accepted through its `rlat_`.
func newForgeAuth(store *memStore, cfg OAuthConfig, principals map[string]*fat.Principal, jwt OAuthTokenValidator) *forgeAuth {
	if jwt == nil {
		jwt = &stubTokenValidator{err: errors.New("not a JWT")}
	}
	return &forgeAuth{
		store: store, tokens: jwt, bindings: store, oauth: cfg,
		credentials: principalIntrospector{principals: principals, inner: store.creds},
		consentBase: "https://app.example.com", logger: slog.Default(),
	}
}

// serveForge runs one request with token through the forge authenticator.
func serveForge(t *testing.T, a *forgeAuth, token string) (*httptest.ResponseRecorder, *forgeObserved) {
	t.Helper()
	var seen *forgeObserved
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = &forgeObserved{session: forgeSessionFrom(r.Context()), callerToken: CallerToken(r.Context())}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, ForgeMountPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.middleware(inner).ServeHTTP(rec, req)
	return rec, seen
}

// requireForgeRefused asserts the forge connector's 401 challenge, with
// nothing reaching the endpoint.
func requireForgeRefused(t *testing.T, rec *httptest.ResponseRecorder, seen *forgeObserved, why string) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", why, rec.Body.String())
	require.Nil(t, seen, "%s: the request reached the endpoint", why)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="reliant-forge"`)
}

// boundForgeStore is a store whose user-1 has bound mcpOAuthClient to a live
// forge grant in org-1.
func boundForgeStore(t *testing.T) *memStore {
	t.Helper()
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(), forgeGrant("g-forge", []string{"shop"}, []string{"envs_list"})))
	bindForge(t, store, mcpOAuthClient, "g-forge")
	return store
}

// The token resolves as an OAuth JWT does, through its client's forge
// binding, and is forwarded unchanged as the caller's token.
func TestForgeMCPForgeTokenWithItsExactAudienceIsAccepted(t *testing.T) {
	store := boundForgeStore(t)
	token := rlatToken(11)

	rec, seen := serveForge(t, newForgeAuth(store, mcpOAuthConfig(), map[string]*fat.Principal{token: mcpForgePrincipal()}, nil), token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, seen)
	assert.Equal(t, "g-forge", seen.session.GrantID)
	assert.Equal(t, "user-1", seen.session.UserID)
	assert.Equal(t, token, seen.callerToken, "the mcp:forge token is what the tools call the control plane with")
}

// End to end: a real MCP client connects with the token, its tool call
// passes the per-call binding check (boundGrant re-resolves the `rlat_`),
// and the control plane is called with that same token.
func TestForgeMCPForgeTokenCallsTheControlPlaneAsItself(t *testing.T) {
	store := boundForgeStore(t)
	token := rlatToken(12)
	creds := &revocableIntrospector{principals: map[string]*fat.Principal{token: mcpForgePrincipal()}}
	url, cp := newForgeAccessTokenServer(t, store, creds)
	sess := connectForge(t, url, token)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "envs_list: %v", res.Content)
	require.NotEmpty(t, cp.bearers())
	for _, a := range cp.bearers() {
		assert.Equal(t, "Bearer "+token, a, "the control plane is called with the client's own mcp:forge token")
	}

	// Revoking the token at the control plane refuses the next call.
	creds.revoke(token)
	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	if err == nil {
		assert.True(t, res.IsError, "a revoked token kept calling")
	}
}

// newForgeAccessTokenServer serves the forge endpoint with acceptance on
// and creds as its introspector, and no JWT validator that accepts anything.
func newForgeAccessTokenServer(t *testing.T, store *memStore, creds CredentialIntrospector) (string, *fakeForgeControlPlane) {
	t.Helper()
	cp := &fakeForgeControlPlane{}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)

	handler, err := NewForgeHTTPHandler(ForgeHTTPDeps{
		Store: store, TokenValidator: &stubTokenValidator{err: errors.New("not a JWT")}, Credentials: creds,
		Bindings: store, OAuth: mcpOAuthConfig(),
		ControlPlaneURL: cpSrv.URL, ConsoleURL: "https://console.example", ConsentBaseURL: "https://app.example.com",
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(ForgeMountPath, handler)
	mux.Handle(ForgeMountPath+"/", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, cp
}

// boundGrant (each tool call's check) accepts the token exactly as a new
// request does, and refuses what a new request refuses.
func TestForgeBoundGrantResolvesAnMCPForgeToken(t *testing.T) {
	store := boundForgeStore(t)
	token, otherOrg, mcpOAuth := rlatToken(13), rlatToken(14), rlatToken(15)
	foreign := mcpForgePrincipal()
	foreign.OrgID = "org-2"
	workspaceToken := mcpOAuthPrincipal()
	workspaceToken.Audience = mcpForgeResource
	a := newForgeAuth(store, mcpOAuthConfig(), map[string]*fat.Principal{
		token: mcpForgePrincipal(), otherOrg: foreign, mcpOAuth: workspaceToken,
	}, nil)

	g, err := a.boundGrant(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "g-forge", g)

	for name, bearer := range map[string]string{"another organization": otherOrg, "mcp:oauth": mcpOAuth} {
		_, err = a.boundGrant(context.Background(), bearer)
		require.ErrorIs(t, err, errForgeGrantGone, name)
	}

	a.oauth.AuthorizationServers = []string{"https://project.supabase.co"}
	_, err = a.boundGrant(context.Background(), token)
	require.ErrorIs(t, err, errForgeGrantGone, "acceptance turned off mid-session")
}

// Consent is per OAuth client, in forge mode: an unbound client is asked.
func TestForgeMCPForgeTokenUsesItsClientsConsent(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(), forgeGrant("g-forge", nil, []string{"envs_list"})))
	token := rlatToken(16)

	rec, seen := serveForge(t, newForgeAuth(store, mcpOAuthConfig(), map[string]*fat.Principal{token: mcpForgePrincipal()}, nil), token)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Nil(t, seen)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Contains(t, body["consent_url"], "kind=forge")
	assert.Contains(t, body["consent_url"], "client_id="+mcpOAuthClient)
}

// The audience binds the token to this endpoint and nothing else (RFC 8707):
// one issued for the workspace connector, another host, a near-miss spelling
// or no resource at all is refused, never narrowed.
func TestForgeMCPForgeTokenForAnotherAudienceIsRefused(t *testing.T) {
	for name, aud := range map[string]string{
		"workspace connector": mcpOAuthResource,
		"other host":          "https://evil.example.com/forge/mcp",
		"trailing slash":      mcpForgeResource + "/",
		"other scheme":        "http://api.example.com/forge/mcp",
		"origin only":         "https://api.example.com",
		"legacy SSE path":     "https://api.example.com/forge/sse",
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			p := mcpForgePrincipal()
			p.Audience = aud
			token := rlatToken(17)

			rec, seen := serveForge(t, newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), map[string]*fat.Principal{token: p}, nil), token)
			requireForgeRefused(t, rec, seen, "audience "+aud)
		})
	}
}

// Acceptance is off unless the control plane is one of the advertised
// authorization servers, and needs an introspector at all.
func TestForgeMCPForgeTokenIsRefusedWhileAcceptanceIsOff(t *testing.T) {
	for name, mutate := range map[string]func(*forgeAuth){
		"control plane not listed": func(a *forgeAuth) { a.oauth.AuthorizationServers = []string{"https://project.supabase.co"} },
		"no control plane":         func(a *forgeAuth) { a.oauth.AccessTokenIssuer = "" },
		"another control plane":    func(a *forgeAuth) { a.oauth.AccessTokenIssuer = "https://other-cp.example.com" },
		"no public URL":            func(a *forgeAuth) { a.oauth.PublicURL = "" },
		"no authorization servers": func(a *forgeAuth) { a.oauth.AuthorizationServers = nil },
		"no introspector":          func(a *forgeAuth) { a.credentials = nil },
	} {
		t.Run(name, func(t *testing.T) {
			token := rlatToken(18)
			a := newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), map[string]*fat.Principal{token: mcpForgePrincipal()}, nil)
			mutate(a)

			rec, seen := serveForge(t, a, token)
			requireForgeRefused(t, rec, seen, name)
		})
	}
}

// No other access token rides the forge path, even when it carries a client
// and this endpoint's audience: the workspace connector's mcp:oauth, a CLI
// token, a daemon's wildcard, a token with no acting user (an org automation
// or operator), an mcp:forge token widened by another scope or bound to a
// resource, one naming no client, and one the control plane calls inactive.
func TestOtherAccessTokensAreRefusedAtTheForgeConnector(t *testing.T) {
	withClient := func(p *fat.Principal) *fat.Principal {
		p.OrgID, p.ClientID, p.Audience = "org-1", mcpOAuthClient, mcpForgeResource
		return p
	}
	for name, p := range map[string]*fat.Principal{
		"mcp:oauth":             withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth)}),
		"reliant:api CLI token": withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeReliantAPI)}),
		"daemon wildcard": withClient(&fat.Principal{
			ActingUserID: "user-1", AllScopes: true, Scopes: fat.SetOf(fat.ScopeMCPForge, fat.ScopeDaemonConnect),
			Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"},
		}),
		"daemon wildcard, unbound": withClient(&fat.Principal{
			ActingUserID: "user-1", AllScopes: true, Scopes: fat.SetOf(fat.ScopeMCPForge),
		}),
		"no acting user (org automation or operator)": withClient(&fat.Principal{Scopes: fat.SetOf(fat.ScopeMCPForge)}),
		"mcp:forge with another scope":                withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPForge, fat.ScopeReliantAPI)}),
		"mcp:forge with mcp:oauth":                    withClient(&fat.Principal{ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPForge, fat.ScopeMCPOAuth)}),
		"mcp:forge with no client": {
			OrgID: "org-1", ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPForge), Audience: mcpForgeResource,
		},
		"mcp:forge bound to a connector": withClient(&fat.Principal{
			ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPForge),
			Resource: &fat.Resource{Kind: fat.ResourceConnector, ID: "g-forge"},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			token := rlatToken(19)
			rec, seen := serveForge(t, newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), map[string]*fat.Principal{token: p}, nil), token)
			requireForgeRefused(t, rec, seen, name)
		})
	}
	t.Run("inactive", func(t *testing.T) {
		rec, seen := serveForge(t, newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), nil, nil), rlatToken(20))
		requireForgeRefused(t, rec, seen, "inactive token")
	})
	t.Run("connector credential", func(t *testing.T) {
		store := boundForgeStore(t)
		raw := store.credentialFor(t, "g-forge", "user-1")
		rec, seen := serveForge(t, newForgeAuth(store, mcpOAuthConfig(), nil, nil), raw)
		requireForgeRefused(t, rec, seen, "connector credential")
	})
}

// A token approved for another organization than its client's forge grant
// reads is sent back to consent (403), never served: the control plane would
// answer it for an organization the grant does not cover. A token naming no
// organization is treated the same.
func TestForgeMCPForgeTokenForAnotherOrganizationRequiresConsent(t *testing.T) {
	for name, org := range map[string]string{"another organization": "org-2", "no organization": ""} {
		t.Run(name, func(t *testing.T) {
			p := mcpForgePrincipal()
			p.OrgID = org
			token := rlatToken(21)

			rec, seen := serveForge(t, newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), map[string]*fat.Principal{token: p}, nil), token)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			require.Nil(t, seen)
			var body map[string]string
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
			assert.Contains(t, body["consent_url"], "kind=forge")
			assert.Contains(t, body["error_description"], "organization")
		})
	}
}

// A JWT the deployment's validator refuses (a managed-identity operator
// token) is refused with acceptance on, and the Supabase OAuth JWT path is
// unchanged by it: no organization check, forwarded as the caller.
func TestForgeJWTsAreUnchangedWithAcceptanceOn(t *testing.T) {
	t.Run("operator JWT", func(t *testing.T) {
		a := newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), nil, &stubTokenValidator{err: errors.New("token is signed by an unknown issuer")})
		rec, seen := serveForge(t, a, "eyJhbGciOiJSUzI1NiJ9.operator.sig")
		requireForgeRefused(t, rec, seen, "operator JWT")
	})
	t.Run("supabase OAuth JWT", func(t *testing.T) {
		a := newForgeAuth(boundForgeStore(t), mcpOAuthConfig(), nil, &stubClientTokenValidator{userID: "user-1", clientID: mcpOAuthClient})
		rec, seen := serveForge(t, a, "a-supabase-jwt")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "g-forge", seen.session.GrantID)
		assert.Equal(t, "a-supabase-jwt", seen.callerToken)
	})
}

// An open session on an mcp:forge token acts only while its client is still
// bound to the grant it opened under, as one on a JWT does.
func TestForgeOpenAccessTokenSessionFollowsTheCurrentBinding(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	require.NoError(t, store.CreateGrant(ctx, forgeGrant("g-broad", nil, []string{"envs_list"})))
	require.NoError(t, store.CreateGrant(ctx, forgeGrant("g-narrow", []string{"not-shop"}, []string{"envs_list"})))
	bindForge(t, store, mcpOAuthClient, "g-broad")
	token := rlatToken(22)
	url, _ := newForgeAccessTokenServer(t, store, &revocableIntrospector{principals: map[string]*fat.Principal{token: mcpForgePrincipal()}})
	sess := connectForge(t, url, token)
	call := func() (*mcp.CallToolResult, error) {
		return sess.CallTool(ctx, &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	}
	res, err := call()
	require.NoError(t, err)
	require.False(t, res.IsError, "the bound grant serves the session")

	bindForge(t, store, mcpOAuthClient, "g-narrow")
	// The rebound client authenticates, so the session itself must refuse.
	res, err = call()
	require.NoError(t, err)
	require.True(t, res.IsError, "the open session kept acting through the grant its client is no longer bound to")
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "reconnect Reliant Forge")
}
