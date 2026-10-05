// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

// acmeManifest is a provider that exists only as YAML: tenant-scoped OAuth
// endpoints, comma-joined scopes, an extra authorize parameter, an identity
// probe that reads nested JSON and treats {"ok": false} as a refusal, and a
// form-encoded revoke. Nothing in Go knows about it.
const acmeManifest = `
id: acme
version: 1
display_name: Acme
connection:
  base_url: "https://{{ connection.params.tenant }}.acme.example.com/api"
  connection_params:
    - { name: tenant, display_name: Tenant, pattern: "[a-z0-9-]{1,30}" }
  auth:
    - oauth2:
        authorize_url: "https://{{ connection.params.tenant }}.acme.example.com/oauth/authorize"
        token_url: "https://{{ connection.params.tenant }}.acme.example.com/oauth/token"
        scopes: [records.read, records.write]
        authorize_params: { access_type: offline }
        revoke:
          url: "https://{{ connection.params.tenant }}.acme.example.com/oauth/revoke"
          token_in: form
    - api_key: { in: query, name: key, label: API key }
    - basic: { username_param: tenant, password_label: Secret }
  probe:
    path: /whoami
    ok: response.ok
    external_id: response.account.id
    label: response.account.name
actions:
  - id: record.get
    placement: server
    params: { type: object }
    request: { method: GET, path: /records }
`

// fakeAcme serves every acme tenant: it records which tenant host each call
// targeted and what it carried.
type fakeAcme struct {
	srv *httptest.Server

	mu        sync.Mutex
	hosts     []string
	tokenForm url.Values
	probeAuth string
	revoked   url.Values
	probeOK   bool
}

func newFakeAcme(t *testing.T) *fakeAcme {
	t.Helper()
	f := &fakeAcme{probeOK: true}
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		f.mu.Lock()
		f.hosts = append(f.hosts, r.Host)
		f.mu.Unlock()
	}
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokenForm = r.PostForm
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acme_at_canary_77", "refresh_token": "acme_rt_1", "expires_in": 3600,
			"scope": "records.read,records.write",
		})
	})
	mux.HandleFunc("/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		f.mu.Lock()
		f.probeAuth = r.Header.Get("Authorization")
		ok := f.probeOK
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "account": map[string]any{"id": "acct-42", "name": "Acme Blue"}})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = r.PostForm
		f.mu.Unlock()
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAcme) lastHost() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hosts) == 0 {
		return ""
	}
	return f.hosts[len(f.hosts)-1]
}

// acmeEnv is the standard env plus the acme provider, whose calls go to its
// own fake.
func acmeEnv(t *testing.T, env map[string]string) (*env, *fakeAcme) {
	t.Helper()
	e := newEnv(t)
	f := newFakeAcme(t)
	providers, err := connections.ProvidersFromCatalog(testManifests(t, acmeManifest), oauthEnv(env))
	require.NoError(t, err)
	doer := hostRouter{"acme.example.com": f.srv, "": e.gh.srv}
	e.providers = providers
	e.tokens = connections.NewTokenSource(e.store, e.vault, providers, doer)
	e.broker = connections.NewBroker(e.store, e.vault, providers, doer, "https://reliant.example")
	e.svc = connections.NewService(e.store, e.vault, providers, e.tokens, e.broker, doer)
	e.resolver = connections.NewResolver(e.repo, e.store, e.tokens)
	return e, f
}

// hostRouter delivers a request to the fake whose key is a suffix of the
// request host ("" matches everything else), keeping the Host header so the
// fake can see which tenant was addressed.
type hostRouter map[string]*httptest.Server

func (h hostRouter) Do(req *http.Request) (*http.Response, error) {
	target := h[""]
	for suffix, srv := range h {
		if suffix != "" && strings.HasSuffix(req.URL.Hostname(), suffix) {
			target = srv
		}
	}
	u, _ := url.Parse(target.URL)
	cp := req.Clone(req.Context())
	cp.URL.Scheme, cp.URL.Host = u.Scheme, u.Host
	cp.Host = req.URL.Host
	return target.Client().Do(cp)
}

var acmeCreds = map[string]string{"RELIANT_OAUTH_ACME_CLIENT_ID": "acme-client", "RELIANT_OAUTH_ACME_CLIENT_SECRET": "acme-secret"}

// The whole OAuth lifecycle for a provider that is only a manifest:
// authorize URL, code exchange, identity probe, labelling, authenticated use,
// test, and revoke on delete — every call at the tenant the user named.
func TestManifestOAuth_FullFlowAtTheTenantHost(t *testing.T) {
	e, f := acmeEnv(t, acmeCreds)
	ctx := context.Background()

	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{
		UserID: "alice", IntegrationID: "acme", Params: map[string]string{"tenant": "blue"},
	})
	require.NoError(t, err)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, "blue.acme.example.com", u.Host)
	require.Equal(t, "/oauth/authorize", u.Path)
	q := u.Query()
	require.Equal(t, "acme-client", q.Get("client_id"))
	require.Equal(t, "records.read records.write", q.Get("scope"), "scopes are space-joined (RFC 6749)")
	require.Equal(t, "offline", q.Get("access_type"), "extra authorize params are sent")
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, "https://reliant.example/integrations/oauth/acme/callback", q.Get("redirect_uri"))

	done, err := e.svc.CompleteOAuth(ctx, "alice", q.Get("state"), "the-code")
	require.NoError(t, err)
	conn := done.Connection
	require.Equal(t, "oauth2", conn.AuthKind)
	require.Equal(t, "acct-42", *conn.ExternalAccountID, "external id read from the probe's JSON path")
	require.Equal(t, "Acme Blue", *conn.AccountLabel)
	require.Equal(t, "Acme Blue", conn.Name, "an unnamed connection takes the probe's label")
	require.Equal(t, map[string]string{"tenant": "blue"}, conn.Params)
	require.Equal(t, []string{"records.read", "records.write"}, conn.Scopes)

	f.mu.Lock()
	form := f.tokenForm
	probeAuth := f.probeAuth
	f.mu.Unlock()
	require.Equal(t, "the-code", form.Get("code"))
	require.Equal(t, "acme-secret", form.Get("client_secret"))
	verifier, err := oauth2.ParseVerifier(form.Get("code_verifier"))
	require.NoError(t, err)
	require.Equal(t, q.Get("code_challenge"), verifier.Challenge().Value)
	require.Equal(t, "Bearer acme_at_canary_77", probeAuth)
	require.Equal(t, "blue.acme.example.com", f.lastHost(), "the probe went to the tenant's base_url")

	// The resolved credential carries the params the runtime templates with.
	run := e.newRun("alice")
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{IntegrationID: "acme"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"tenant": "blue"}, got.Params())
	req, _ := http.NewRequest(http.MethodGet, "https://blue.acme.example.com/api/records", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "Bearer acme_at_canary_77", req.Header.Get("Authorization"))

	// TestConnection re-runs the probe; {"ok": false} is a refusal.
	res, err := e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.True(t, res.Probed)
	f.mu.Lock()
	f.probeOK = false
	f.mu.Unlock()
	res, err = e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.False(t, res.OK)
	require.Equal(t, "unauthorized", res.ErrorClass)

	// Delete revokes at the provider, form-encoded per the manifest.
	require.NoError(t, e.svc.Delete(ctx, "alice", conn.ID))
	f.mu.Lock()
	revoked := f.revoked
	f.mu.Unlock()
	require.Equal(t, "acme_at_canary_77", revoked.Get("token"))
	require.Equal(t, "blue.acme.example.com", f.lastHost())
}

// scope_separator "," loads (it is valid manifest data), but the flow refuses
// to start until forge's AuthRequest can send a comma-delimited scope, rather
// than sending a string the provider will misread.
func TestManifestOAuth_CommaScopesRefusedUntilSupported(t *testing.T) {
	e := newEnv(t)
	comma := strings.Replace(acmeManifest, "        scopes: [records.read, records.write]\n", "        scopes: [records.read, records.write]\n        scope_separator: \",\"\n", 1)
	providers, err := connections.ProvidersFromCatalog(testManifests(t, comma), oauthEnv(acmeCreds))
	require.NoError(t, err)
	b := connections.NewBroker(e.store, e.vault, providers, nil, "https://reliant.example")
	_, err = b.Start(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "acme", Binder: "b", Params: map[string]string{"tenant": "blue"}})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "comma-separated scopes")
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))
}

// A tenant that is not a single DNS label, or does not match the declared
// pattern, never becomes a URL.
func TestManifestOAuth_ParamsAreValidated(t *testing.T) {
	e, _ := acmeEnv(t, acmeCreds)
	ctx := context.Background()
	for name, params := range map[string]map[string]string{
		"missing":         nil,
		"smuggles a host": {"tenant": "evil.com"},
		"off pattern":     {"tenant": "UPPER_case"},
		"undeclared":      {"tenant": "blue", "region": "eu"},
	} {
		_, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: "alice", IntegrationID: "acme", Params: params})
		require.ErrorIs(t, err, connections.ErrInvalidArgument, name)
	}
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))
}

// Without RELIANT_OAUTH_ACME_CLIENT_ID/SECRET the oauth2 method is listed but
// unavailable with an operator reason; the pasted methods still work.
func TestManifestOAuth_UnconfiguredIsListedUnavailable(t *testing.T) {
	e, _ := acmeEnv(t, nil)
	var acme *connections.Integration
	for _, i := range e.svc.ListIntegrations() {
		if i.ID == "acme" {
			i := i
			acme = &i
		}
	}
	require.NotNil(t, acme)
	require.Equal(t, "Acme", acme.DisplayName)
	require.Len(t, acme.Methods, 3)
	require.Equal(t, connections.MethodOAuth2, acme.Methods[0].Kind)
	require.False(t, acme.Methods[0].Available)
	require.Equal(t, "RELIANT_OAUTH_ACME_CLIENT_ID and RELIANT_OAUTH_ACME_CLIENT_SECRET are not set", acme.Methods[0].Reason)
	require.True(t, acme.Methods[1].Available)
	require.Equal(t, map[string]string{"api_key": "API key"}, acme.Methods[1].FieldLabels)
	require.Equal(t, map[string]string{"password": "Secret"}, acme.Methods[2].FieldLabels, "the username comes from the tenant param")
	require.Len(t, acme.Params, 1)

	_, err := e.svc.StartOAuth(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "acme", Params: map[string]string{"tenant": "blue"}})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "RELIANT_OAUTH_ACME_CLIENT_ID")
}

// An api_key declared `in: query` goes in the query string, never a header,
// and the key is scrubbed from echoes.
func TestManifestAPIKey_InQuery(t *testing.T) {
	e, _ := acmeEnv(t, nil)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "acme", Name: "k", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "qk_canary_123"}, Params: map[string]string{"tenant": "red"},
	})
	require.NoError(t, err)
	require.Nil(t, conn.AuthHeader, "placement is the manifest's; nothing per connection")
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)
	require.Equal(t, "red", got.Params()["tenant"])
	req, _ := http.NewRequest(http.MethodGet, "https://red.acme.example.com/api/records?page=2", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "qk_canary_123", req.URL.Query().Get("key"))
	require.Equal(t, "2", req.URL.Query().Get("page"), "existing query is kept")
	require.Empty(t, req.Header.Get("Authorization"))
	require.NotContains(t, got.Redactor.Scrub("GET "+req.URL.String()), "qk_canary_123")
}

// A custom header with a prefix: GitHub's PAT method writes
// "Authorization: Bearer <key>"; a fixed header name with no prefix works too.
func TestManifestAPIKey_CustomHeaderAndPrefix(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "github", Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "ghp_canary"},
	})
	require.NoError(t, err)
	require.Equal(t, "api_key", conn.AuthKind)
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "Bearer ghp_canary", req.Header.Get("Authorization"))

	// The PAT is probed with the manifest's identity probe, same as OAuth.
	res, err := e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.True(t, res.Probed)
	require.True(t, res.OK)
	require.Equal(t, "octocat", res.AccountLabel)
	e.gh.mu.Lock()
	require.Equal(t, "Bearer ghp_canary", e.gh.probeAuth)
	e.gh.mu.Unlock()
}

// Basic auth whose username is a connection param: the user pastes only the
// secret, and the request carries param:secret.
func TestManifestBasic_UsernameFromParam(t *testing.T) {
	e, _ := acmeEnv(t, nil)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "acme", Name: "b", Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"password": "pw_canary"}, Params: map[string]string{"tenant": "green"},
	})
	require.NoError(t, err)
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://green.acme.example.com/api/records", nil)
	require.NoError(t, got.Apply(req))
	user, pass, ok := req.BasicAuth()
	require.True(t, ok)
	require.Equal(t, "green", user)
	require.Equal(t, "pw_canary", pass)

	// A username field is refused: it is the param's to supply.
	_, err = e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "acme", Name: "b2", Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"username": "x", "password": "p"}, Params: map[string]string{"tenant": "green"},
	})
	require.ErrorIs(t, err, connections.ErrInvalidArgument)
}

// The generic HTTP shape: an integration that leaves header placement to each
// connection keeps the closed allow-list, and nothing else gets one.
func TestManifestAPIKey_OpenPlacementKeepsAllowList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for choice, want := range map[string][2]string{
		"":          {"Authorization", "Bearer k1"},
		"x-api-key": {"X-Api-Key", "k1"},
	} {
		fields := map[string]string{"api_key": "k1"}
		if choice != "" {
			fields["header"] = choice
		}
		conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{UserID: "alice", IntegrationID: "svc", Name: "n" + choice, Kind: connections.APIKeyKindAPIKey, Fields: fields})
		require.NoError(t, err)
		got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
		require.NoError(t, err)
		req, _ := http.NewRequest(http.MethodGet, "https://x.example", nil)
		require.NoError(t, got.Apply(req))
		require.Equal(t, want[1], req.Header.Get(want[0]))
	}
	_, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{UserID: "alice", IntegrationID: "svc", Name: "c", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "k", "header": "Cookie"}})
	require.ErrorIs(t, err, connections.ErrInvalidArgument)
}
