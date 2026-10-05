// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/stretchr/testify/require"
)

const (
	clientID     = "Iv1.testclient"
	clientSecret = "client-secret-canary-0001"
)

// testCatalog is what every test registry is built from: integrations are
// manifests, exactly as in production. Its URLs are the real https hosts; the
// redirectingDoer delivers them to the fake server.
const testCatalog = `
id: github
version: 1
display_name: GitHub
connection:
  base_url: https://api.github.com
  default_headers: { Accept: application/vnd.github+json }
  auth:
    - oauth2:
        authorize_url: https://github.com/login/oauth/authorize
        token_url: https://github.com/login/oauth/access_token
        revoke:
          method: DELETE
          url: "https://api.github.com/applications/{{ client_id }}/token"
          client_auth: basic
          token_in: json
          token_param: access_token
    - api_key: { in: header, name: Authorization, prefix: "Bearer ", label: Personal access token }
  probe:
    path: /user
    external_id: string(response.id)
    label: response.login
actions:
  - id: user.get
    placement: server
    params: { type: object }
    request: { method: GET, path: /user }
---
id: svc
version: 1
display_name: Svc
connection:
  allow_any_public_host: true
  auth_optional: true
  auth:
    - api_key: {}
    - basic: {}
actions:
  - id: request
    placement: server
    params: { type: object, properties: { url: { type: string } } }
    request: { method: GET, url: "{{ params.url }}" }
`

// testManifests parses the catalog plus any extra documents, a "---" apart.
func testManifests(t *testing.T, extra ...string) []*reliantv1.IntegrationManifest {
	t.Helper()
	var out []*reliantv1.IntegrationManifest
	for _, doc := range strings.Split(strings.Join(append([]string{testCatalog}, extra...), "\n---\n"), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		m, err := manifest.Parse([]byte(doc), manifest.TrustCurated)
		require.NoError(t, err)
		out = append(out, m)
	}
	return out
}

// oauthEnv is the deployment config the registry reads client credentials from.
func oauthEnv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// fakeGitHub is an httptest TLS server standing in for github.com. No test in
// this package touches the real network.
type fakeGitHub struct {
	srv *httptest.Server

	mu            sync.Mutex
	tokenCalls    atomic.Int32
	refreshCalls  atomic.Int32
	lastForm      url.Values
	refreshStatus int    // 0 = ok
	refreshBody   string // overrides the body when refreshStatus != 0
	refreshDelay  time.Duration
	exchangeFail  bool
	nextAccess    string
	nextRefresh   string
	expiresIn     int64
	login         string
	accountID     int64
	seq           atomic.Int64
	probeAuth     string
	revoked       []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{login: "octocat", accountID: 583231, expiresIn: 28800}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", g.token)
	mux.HandleFunc("/applications/", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		user, pass, _ := r.BasicAuth()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.revoked = append(g.revoked, r.Method+" "+r.URL.Path+" "+user+":"+pass+" "+body["access_token"])
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.probeAuth = r.Header.Get("Authorization")
		g.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": g.accountID, "login": g.login})
	})
	g.srv = httptest.NewTLSServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.mu.Lock()
	g.lastForm = r.PostForm
	g.mu.Unlock()
	g.tokenCalls.Add(1)
	w.Header().Set("Content-Type", "application/json")
	n := g.seq.Add(1)
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if g.exchangeFail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"the code ` + r.PostForm.Get("code") + ` is incorrect"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ghu_access_" + itoa(n), "refresh_token": "ghr_refresh_" + itoa(n),
			"expires_in": g.expiresIn, "refresh_token_expires_in": 15811200, "token_type": "bearer",
		})
	case "refresh_token":
		g.refreshCalls.Add(1)
		if g.refreshDelay > 0 {
			time.Sleep(g.refreshDelay)
		}
		if g.refreshStatus != 0 {
			w.WriteHeader(g.refreshStatus)
			_, _ = w.Write([]byte(g.refreshBody))
			return
		}
		access, refresh := "ghu_access_"+itoa(n), "ghr_refresh_"+itoa(n)
		if g.nextAccess != "" {
			access = g.nextAccess
		}
		if g.nextRefresh != "" {
			refresh = g.nextRefresh
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "refresh_token": refresh, "expires_in": g.expiresIn, "token_type": "bearer",
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported_grant_type"}`))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// redirectingDoer sends every provider call to the fake server whatever host
// the catalog names, trusting the fake's self-signed certificate.
type redirectingDoer struct {
	target *httptest.Server
}

func (d redirectingDoer) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(d.target.URL)
	cp := req.Clone(req.Context())
	cp.URL.Scheme, cp.URL.Host, cp.Host = u.Scheme, u.Host, u.Host
	return d.target.Client().Do(cp)
}

type env struct {
	t         *testing.T
	repo      *db.Repo
	raw       *sql.DB
	vault     *vault.Vault
	store     core.ConnectionStore
	gh        *fakeGitHub
	providers *connections.Registry
	tokens    *connections.TokenSource
	broker    *connections.Broker
	svc       *connections.Service
	resolver  *connections.Resolver
	now       func() time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	v := vault.New(raw, vault.NewEnvKeyWrapper(ring))

	gh := newFakeGitHub(t)
	providers, err := connections.ProvidersFromCatalog(testManifests(t), oauthEnv(map[string]string{
		"RELIANT_OAUTH_GITHUB_CLIENT_ID": clientID, "RELIANT_OAUTH_GITHUB_CLIENT_SECRET": clientSecret,
	}))
	require.NoError(t, err)
	doer := redirectingDoer{target: gh.srv}

	store := repo.Connections()
	tokens := connections.NewTokenSource(store, v, providers, doer)
	broker := connections.NewBroker(store, v, providers, doer, "https://reliant.example")
	svc := connections.NewService(store, v, providers, tokens, broker, doer)
	return &env{
		t: t, repo: repo, raw: raw, vault: v, store: store, gh: gh, providers: providers,
		tokens: tokens, broker: broker, svc: svc,
		resolver: connections.NewResolver(repo, store, tokens),
		now:      time.Now,
	}
}

// connect completes a full GitHub OAuth flow for userID and returns the connection.
func (e *env) connect(userID, name string) *core.Connection {
	e.t.Helper()
	ctx := context.Background()
	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: userID, IntegrationID: "github", Name: name})
	require.NoError(e.t, err)
	state := mustQuery(e.t, authURL, "state")
	done, err := e.svc.CompleteOAuth(ctx, userID, state, "authcode")
	require.NoError(e.t, err)
	return done.Connection
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	v := u.Query().Get(key)
	require.NotEmpty(t, v, "missing %s in %s", key, raw)
	return v
}

// newRun creates a run owned by userID and returns its id.
func (e *env) newRun(userID string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID := uuid.NewString()
	require.NoError(e.t, e.repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "t", ProjectID: "test-project", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := e.repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(e.t, err)
	owner := userID
	require.NoError(e.t, e.repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: db.Active(), CreatedAt: now, OwnerUserID: &owner,
	}))
	return chatID
}

func (e *env) expireAccessToken(connID string) {
	e.t.Helper()
	_, err := e.raw.Exec(`UPDATE connections SET access_expires_at = now() - interval '1 minute' WHERE id = $1`, connID)
	require.NoError(e.t, err)
}

func (e *env) generation(connID, field string) int64 {
	e.t.Helper()
	var g int64
	require.NoError(e.t, e.raw.QueryRow(`SELECT generation FROM connection_secrets WHERE connection_id=$1 AND field=$2`, connID, field).Scan(&g))
	return g
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.raw.QueryRow(query, args...).Scan(&n))
	return n
}
