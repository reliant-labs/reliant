// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

func testOAuthConfig() OAuthConfig {
	return OAuthConfig{
		PublicURL:            "https://api.example.com",
		AuthorizationServers: []string{"https://project.supabase.co"},
	}
}

// TestDiscoveryDocumentShape checks the fields a third-party client parses.
// These are a contract with software we do not control, so a silent change
// surfaces as a connector that simply stops working.
func TestDiscoveryDocumentShape(t *testing.T) {
	srv := httptest.NewServer(NewProtectedResourceMetadataHandler(testOAuthConfig()))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var doc map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))

	// The resource identifier must be the canonical MCP endpoint URI, since
	// that is what a client sends back as the RFC 8707 resource parameter.
	require.Equal(t, "https://api.example.com/mcp", doc["resource"])
	require.Contains(t, doc["authorization_servers"], "https://project.supabase.co")
	require.Contains(t, doc["bearer_methods_supported"], "header")
}

// TestDiscoveryIsCORSEnabled: browser-based clients fetch this cross-origin,
// and without CORS the discovery step fails in a way that looks like the
// server being down.
func TestDiscoveryIsCORSEnabled(t *testing.T) {
	srv := httptest.NewServer(NewProtectedResourceMetadataHandler(testOAuthConfig()))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	req, err := http.NewRequest(http.MethodOptions, srv.URL, nil)
	require.NoError(t, err)
	preflight, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = preflight.Body.Close() }()
	require.Equal(t, http.StatusNoContent, preflight.StatusCode)
	require.Contains(t, preflight.Header.Get("Access-Control-Allow-Methods"), "GET")
}

func TestOAuthRoutesMountedAtBothPaths(t *testing.T) {
	mux := http.NewServeMux()
	MountOAuthRoutes(mux, testOAuthConfig(), nil)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Clients differ on which path they probe first; a 404 during discovery is
	// opaque to debug from a phone.
	for _, path := range []string{
		WellKnownProtectedResourcePath,
		WellKnownProtectedResourcePath + MountPath,
	} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "path %s should serve metadata", path)
	}
}

// TestDiscoveryNotMountedWithoutIssuer: advertising a document that names no
// authorization server would send a client into a flow that cannot complete.
func TestDiscoveryNotMountedWithoutIssuer(t *testing.T) {
	mux := http.NewServeMux()
	MountOAuthRoutes(mux, OAuthConfig{PublicURL: "https://api.example.com"}, nil)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + WellKnownProtectedResourcePath)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestChallengePointsAtDiscovery is what turns a rejection into a usable
// discovery step, rather than leaving a client to probe and guess.
func TestChallengePointsAtDiscovery(t *testing.T) {
	header := testOAuthConfig().challengeHeader()
	require.Contains(t, header, `resource_metadata="https://api.example.com/.well-known/oauth-protected-resource"`)
	require.Contains(t, header, "Bearer")

	// With no issuer configured there is nothing to point at, and the header
	// must not advertise a flow that cannot complete.
	bare := OAuthConfig{}.challengeHeader()
	require.Contains(t, bare, "Bearer")
	require.NotContains(t, bare, "resource_metadata")
}

// TestAdvertisedScopesMatchTheAuthorizationServer pins the rule this got wrong
// once: a resource server may only advertise scopes its AS will actually
// issue.
//
// We previously advertised an invented `mcp` scope. Supabase rejected it at
// the authorize step — "unsupported scope: mcp" — so the flow died before the
// user ever saw a consent screen. `openid` is the one scope every OIDC
// provider supports, and it is sufficient because the token establishes WHO is
// calling while the connector grant establishes what they may touch.
func TestAdvertisedScopesMatchTheAuthorizationServer(t *testing.T) {
	srv := httptest.NewServer(NewProtectedResourceMetadataHandler(testOAuthConfig()))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var doc struct {
		ScopesSupported []string `json:"scopes_supported"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"openid"}, doc.ScopesSupported,
		"advertising a scope the authorization server does not issue breaks the flow at /authorize")

	// The challenge must name the same scopes, or a client following it asks
	// for something the AS will refuse.
	require.Contains(t, testOAuthConfig().challengeHeader(), `scope="openid"`)
}

func TestScopesAreConfigurable(t *testing.T) {
	cfg := testOAuthConfig()
	cfg.Scopes = []string{"openid", "profile"}

	srv := httptest.NewServer(NewProtectedResourceMetadataHandler(cfg))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var doc struct {
		ScopesSupported []string `json:"scopes_supported"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"openid", "profile"}, doc.ScopesSupported)
	require.Contains(t, cfg.challengeHeader(), `scope="openid profile"`)
}

// stubTokenValidator stands in for the deployment's JWT validator.
type stubTokenValidator struct {
	userID string
	err    error
}

func (s *stubTokenValidator) ValidateToken(string) (string, error) {
	return s.userID, s.err
}

// newOAuthTestServer starts the MCP endpoint with OAuth enabled and returns
// the URL plus the store, so tests can vary the user's grants.
func newOAuthTestServer(t *testing.T, validator OAuthTokenValidator) (string, *memStore) {
	t.Helper()

	store := newMemStore()
	handler, err := NewHTTPHandler(HTTPDeps{
		Store:          store,
		Sender:         &fakeSender{},
		OAuth:          testOAuthConfig(),
		TokenValidator: validator,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL, store
}

func addGrant(t *testing.T, store *memStore, userID, name string) *connectorgrant.Grant {
	t.Helper()
	_, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	g := &connectorgrant.Grant{
		ID:           name,
		UserID:       userID,
		DaemonID:     "daemon-1",
		Name:         name,
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}
	require.NoError(t, store.CreateGrant(context.Background(), g))
	return g
}

func postWithToken(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+MountPath, nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestOAuthTokenResolvesToTheUsersConnector is the consumer-mobile path: the
// token says who you are, and the GRANT says what you may touch.
func TestOAuthTokenResolvesToTheUsersConnector(t *testing.T) {
	url, store := newOAuthTestServer(t, &stubTokenValidator{userID: "user-1"})
	addGrant(t, store, "user-1", "phone")

	resp := postWithToken(t, url, "an-oauth-access-token")
	defer func() { _ = resp.Body.Close() }()

	require.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"a valid OAuth token with one active connector should authenticate")
}

// TestOAuthTokenWithNoConnectorNeedsConsent: a token proves identity, not
// authority. With no connector there is nothing to act through, and the fix is
// for the user to choose one — not for the client to re-authenticate.
func TestOAuthTokenWithNoConnectorNeedsConsent(t *testing.T) {
	url, _ := newOAuthTestServer(t, &stubTokenValidator{userID: "user-1"})

	resp := postWithToken(t, url, "an-oauth-access-token")
	defer func() { _ = resp.Body.Close() }()

	// 403, not 401: the caller IS authenticated. A 401 would send a
	// well-behaved client back through the token flow, which cannot help.
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["consent_url"], ConsentPath,
		"the response must tell the client where its user can choose")
}

// TestAmbiguousSelectionRequiresConsent: with more than one live connector
// there is a real choice, and only the user can make it. Guessing would hand
// an application authority its user never intended, invisibly.
func TestAmbiguousSelectionRequiresConsent(t *testing.T) {
	url, store := newOAuthTestServer(t, &stubTokenValidator{userID: "user-1"})
	addGrant(t, store, "user-1", "phone")
	addGrant(t, store, "user-1", "laptop")

	resp := postWithToken(t, url, "an-oauth-access-token")
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an ambiguous connector choice must not be resolved by guessing")

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["consent_url"], ConsentPath)
}

// TestRecordedConsentResolvesTheAmbiguity is the payoff: once the user has
// chosen, the same request that needed consent now works, and keeps working
// without asking again.
func TestRecordedConsentResolvesTheAmbiguity(t *testing.T) {
	store := newMemStore()
	chosen := addGrant(t, store, "user-1", "phone")
	addGrant(t, store, "user-1", "laptop")

	handler, err := NewHTTPHandler(HTTPDeps{
		Store:          store,
		Sender:         &fakeSender{},
		OAuth:          testOAuthConfig(),
		TokenValidator: &stubTokenValidator{userID: "user-1"},
		Bindings:       store,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Without a recorded choice, consent is required.
	resp := postWithClient(t, srv.URL, "an-oauth-access-token", "chatgpt")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// The user chooses.
	require.NoError(t, store.PutBinding(context.Background(), &connectorgrant.ClientBinding{
		ID:       "binding-1",
		UserID:   "user-1",
		ClientID: "chatgpt",
		GrantID:  chosen.ID,
	}))

	resp = postWithClient(t, srv.URL, "an-oauth-access-token", "chatgpt")
	defer func() { _ = resp.Body.Close() }()
	require.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"a recorded consent choice must resolve the ambiguity")
	require.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestConsentIsPerClient: authorizing one application must not silently
// authorize another.
func TestConsentIsPerClient(t *testing.T) {
	store := newMemStore()
	chosen := addGrant(t, store, "user-1", "phone")
	addGrant(t, store, "user-1", "laptop")

	handler, err := NewHTTPHandler(HTTPDeps{
		Store:          store,
		Sender:         &fakeSender{},
		OAuth:          testOAuthConfig(),
		TokenValidator: &stubTokenValidator{userID: "user-1"},
		Bindings:       store,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	require.NoError(t, store.PutBinding(context.Background(), &connectorgrant.ClientBinding{
		ID: "binding-1", UserID: "user-1", ClientID: "chatgpt", GrantID: chosen.ID,
	}))

	resp := postWithClient(t, srv.URL, "an-oauth-access-token", "some-other-app")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a different application must obtain its own consent")
}

// postWithClient sends a request identifying the calling application.
func postWithClient(t *testing.T, url, token, clientID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+MountPath, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-MCP-Client-Id", clientID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestInvalidOAuthTokenIsRejected(t *testing.T) {
	url, store := newOAuthTestServer(t, &stubTokenValidator{err: errors.New("bad signature")})
	addGrant(t, store, "user-1", "phone")

	resp := postWithToken(t, url, "a-forged-token")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestConnectorCredentialsStillWorkWithOAuthEnabled: turning OAuth on must not
// break the path that works today from Claude Desktop and the API.
func TestConnectorCredentialsStillWorkWithOAuthEnabled(t *testing.T) {
	store := newMemStore()
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID:           "grant-cred",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}))

	handler, err := NewHTTPHandler(HTTPDeps{
		Store:          store,
		Sender:         &fakeSender{},
		OAuth:          testOAuthConfig(),
		TokenValidator: &stubTokenValidator{err: errors.New("not an oauth token")},
	})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp := postWithToken(t, srv.URL, raw)
	defer func() { _ = resp.Body.Close() }()
	require.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"a connector credential must keep working when OAuth is enabled")
}

// TestNoOAuthValidatorRejectsOAuthTokens: without a validator, a non-connector
// token has no basis to be trusted.
func TestNoOAuthValidatorRejectsOAuthTokens(t *testing.T) {
	url, store := newOAuthTestServer(t, nil)
	addGrant(t, store, "user-1", "phone")

	resp := postWithToken(t, url, "some-jwt")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
