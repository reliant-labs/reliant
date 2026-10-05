// Copyright (c) 2025 Reliant Labs

package ghdelegated

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/vault"
)

// fakeCP stands in for control-plane: each user's current token, and a log
// of whose token was asked for.
type fakeCP struct {
	mu     sync.Mutex
	tokens map[string]string
	err    error
	asked  []string
}

func (f *fakeCP) UserAccessToken(_ context.Context, externalUserID, provider string) (gitcredentialclient.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, externalUserID+"|"+provider)
	if f.err != nil {
		return gitcredentialclient.Token{}, f.err
	}
	tok, ok := f.tokens[externalUserID]
	if !ok {
		return gitcredentialclient.Token{}, gitcredentialclient.ErrNotConnected
	}
	exp := time.Now().Add(8 * time.Hour)
	return gitcredentialclient.NewToken(tok, &exp), nil
}

type env struct {
	t        *testing.T
	repo     *db.Repo
	svc      *connections.Service
	resolver *connections.Resolver
	cp       *fakeCP
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
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), func(string) string { return "" })
	require.NoError(t, err)
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, v, providers, nil)
	resolver := connections.NewResolver(repo, store, tokens)
	broker := connections.NewBroker(store, v, providers, nil, "https://reliant.example")
	svc := connections.NewService(store, v, providers, tokens, broker, nil)
	return &env{t: t, repo: repo, svc: svc, resolver: resolver, cp: &fakeCP{tokens: map[string]string{
		"idp|alice": "ghu_alice_token_9f3c1e",
		"idp|bob":   "ghu_bob_token_7a2d4b",
	}}}
}

// newRun records a run owned by userID — the run record is the ONLY source of
// whose token is used.
func (e *env) newRun(userID string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	require.NoError(e.t, e.repo.CreateChat(ctx, &db.Chat{ID: id, Title: "t", ProjectID: "test-project", UserID: userID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := e.repo.CreateThread(ctx, &db.Thread{ID: id, ChatID: id, CreatedAt: now})
	require.NoError(e.t, err)
	owner := userID
	require.NoError(e.t, e.repo.CreateWorkflow(ctx, &db.Workflow{ID: id, ChatID: id, WorkflowName: "builtin://agent", Thread: id, Status: db.Active(), CreatedAt: now, OwnerUserID: &owner}))
	return id
}

// source is the hosted composition: the broker registered under BrokerID,
// which the `github` manifest's delegated method names. host pins the
// credential to the test server instead of api.github.com.
func (e *env) source(host string) *connauth.Source {
	brokers := connauth.NewBrokers()
	require.NoError(e.t, brokers.Register(BrokerID, newBroker(e.cp, host, nil)))
	return connauth.New(e.resolver).WithBrokers(brokers)
}

// githubSpec is the embedded `github` manifest's connection declaration.
func githubSpec(t *testing.T) *reliantv1.ConnectionSpec {
	t.Helper()
	a, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	return a.Manifest.GetConnection()
}

func githubReq(t *testing.T, runID string) httpaction.CredentialRequest {
	return httpaction.CredentialRequest{RunID: runID, IntegrationID: IntegrationID, Connection: githubSpec(t), ServerPlaced: true}
}

func applied(t *testing.T, cred httpaction.Credential, rawURL string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	require.NoError(t, cred.Apply(req))
	return req.Header.Get("Authorization")
}

// THE property: the token used is the RUN OWNER's — read from the run record —
// and nothing else. Alice's run gets Alice's token; Bob's run gets Bob's.
func TestResolvesOnlyForTheRunOwner(t *testing.T) {
	e := newEnv(t)
	src := e.source(APIHost)

	aliceCred, err := src.Credential(context.Background(), githubReq(t, e.newRun("idp|alice")))
	require.NoError(t, err)
	assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", applied(t, aliceCred, "https://api.github.com/user"))

	bobCred, err := src.Credential(context.Background(), githubReq(t, e.newRun("idp|bob")))
	require.NoError(t, err)
	assert.Equal(t, "Bearer ghu_bob_token_7a2d4b", applied(t, bobCred, "https://api.github.com/user"))

	assert.Equal(t, []string{"idp|alice|github", "idp|bob|github"}, e.cp.asked,
		"control-plane is asked for exactly the run owners, by external id")
}

// Nothing in the request can name another user: a connection id that is
// really someone's user id resolves as no connection and is refused, and the
// delegated path reads the owner from the run.
func TestRequestCannotNameAnotherUser(t *testing.T) {
	e := newEnv(t)
	req := githubReq(t, e.newRun("idp|alice"))
	req.ConnectionID = "idp|bob"
	_, err := e.source(APIHost).Credential(context.Background(), req)
	require.Error(t, err)
	assert.Empty(t, e.cp.asked, "bob's token is never fetched")

	req.ConnectionID = ""
	cred, err := e.source(APIHost).Credential(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", applied(t, cred, "https://api.github.com/repos/o/r"))
	assert.Equal(t, []string{"idp|alice|github"}, e.cp.asked)
}

// A run that does not exist, or has no owner, resolves for no one.
func TestUnknownRunResolvesForNoOne(t *testing.T) {
	e := newEnv(t)
	_, err := e.source(APIHost).Credential(context.Background(), githubReq(t, uuid.NewString()))
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce), "got %v", err)
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Empty(t, e.cp.asked, "control-plane was never asked")
}

// A token must never leave the server.
func TestDaemonPlacedCallIsRefused(t *testing.T) {
	e := newEnv(t)
	req := githubReq(t, e.newRun("idp|alice"))
	req.ServerPlaced = false
	_, err := e.source(APIHost).Credential(context.Background(), req)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Empty(t, e.cp.asked)
}

func TestOwnerWithoutGitHub_IsNotConnected(t *testing.T) {
	e := newEnv(t)
	_, err := e.source(APIHost).Credential(context.Background(), githubReq(t, e.newRun("idp|carol")))
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Contains(t, ce.Message, "GitHub is not connected")
}

func TestControlPlaneErrorsMapToCredentialCodes(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"needs reconnect": {gitcredentialclient.ErrNeedsReconnect, httpaction.CodeNeedsReauth},
		"outage":          {&gitcredentialclient.RPCError{Code: "unavailable", Message: "http://admin-server:8090 down"}, httpaction.CodeUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.cp.err = tc.err
			_, err := e.source(APIHost).Credential(context.Background(), githubReq(t, e.newRun("idp|alice")))
			var ce *httpaction.CredentialError
			require.True(t, errors.As(err, &ce))
			assert.Equal(t, tc.want, ce.Code)
			assert.NotContains(t, ce.Message, "admin-server", "an internal URL never reaches the user")
		})
	}
}

// The broker is asked only for an integration whose manifest names it, and
// only when the owner has no saved connection. Self-hosted (no broker
// registered) `github` is a saved connection or nothing.
func TestBrokerOnlyForItsIntegrationAndSelfHostedFallsThrough(t *testing.T) {
	e := newEnv(t)
	run := e.newRun("idp|alice")

	// Another integration (generic http, no delegated method) never reaches CP.
	_, err := e.source(APIHost).Credential(context.Background(), httpaction.CredentialRequest{
		RunID: run, IntegrationID: "http", ConnectionID: "c1", ServerPlaced: true,
		Connection: &reliantv1.ConnectionSpec{AllowAnyPublicHost: true, AuthOptional: true},
	})
	require.Error(t, err)
	assert.Empty(t, e.cp.asked, "control-plane is consulted only for an integration that names its broker")

	// Self-hosted: no broker registered, so github without a saved connection
	// is "no github connection", not a control-plane call.
	_, err = connauth.New(e.resolver).Credential(context.Background(), githubReq(t, run))
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Contains(t, ce.Message, "no github connection")
	assert.Empty(t, e.cp.asked)
}

// A saved GitHub connection (a pasted token, or reliant's own OAuth app) wins
// over the delegated authority: an explicit choice the owner made.
func TestSavedConnectionWinsOverDelegated(t *testing.T) {
	e := newEnv(t)
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: "idp|alice", IntegrationID: "github", Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "ghp_saved_pat"},
	})
	require.NoError(t, err)
	cred, err := e.source(APIHost).Credential(context.Background(), githubReq(t, e.newRun("idp|alice")))
	require.NoError(t, err)
	assert.Equal(t, conn.ID, cred.ConnectionID())
	assert.Equal(t, "Bearer ghp_saved_pat", applied(t, cred, "https://api.github.com/user"))
	assert.Empty(t, e.cp.asked)
}

// An explicit connection id that does not resolve is an error, never a
// silent switch to the owner's delegated token.
func TestExplicitMissingConnectionIsNotRedirectedToBroker(t *testing.T) {
	e := newEnv(t)
	req := githubReq(t, e.newRun("idp|alice"))
	req.ConnectionID = "conn_nope"
	_, err := e.source(APIHost).Credential(context.Background(), req)
	require.Error(t, err)
	assert.Empty(t, e.cp.asked)
}

// The credential is pinned: it is applied only to https://api.github.com.
func TestCredentialIsPinnedToAPIGitHubCom(t *testing.T) {
	e := newEnv(t)
	cred, err := e.source(APIHost).Credential(context.Background(), githubReq(t, e.newRun("idp|alice")))
	require.NoError(t, err)
	for _, bad := range []string{
		"https://evil.example/user",
		"http://api.github.com/user",
		"https://api.github.com.evil.example/user",
		"https://uploads.github.com/repos/o/r",
	} {
		req, _ := http.NewRequest(http.MethodGet, bad, nil)
		err := cred.Apply(req)
		require.Error(t, err, bad)
		assert.Empty(t, req.Header.Get("Authorization"), "no token was written for %s", bad)
	}
	assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", applied(t, cred, "https://API.GITHUB.COM/user"))
	assert.Equal(t, ConnectionID, cred.ConnectionID())
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", cred, cred, cred), "ghu_alice")
}

// End to end through the HTTP runtime: the token reaches the pinned host as a
// bearer, never a second host, and is scrubbed from everything the call
// returns — including a hostile upstream that echoes it back.
func TestEndToEnd_BearerAppliedPinnedAndScrubbed(t *testing.T) {
	e := newEnv(t)
	var seen string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// A hostile upstream reflects the token into a field the action selects.
		_, _ = fmt.Fprintf(w, `{"login":"alice","id":583231,"name":%q}`, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	src := e.source(u.Host)

	m, a := githubManifest(srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	runner := httpaction.NewRunner(g).WithRootCAs(pool)

	res, err := runner.RunAuthenticated(context.Background(), m, a, map[string]any{}, src,
		httpaction.CallSite{RunID: e.newRun("idp|alice")})
	require.NoError(t, err)
	assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", seen, "the owner's token reached the host as a bearer")
	assert.NotContains(t, res.Content, "ghu_alice_token_9f3c1e", "scrubbed from the result")
	assert.Contains(t, res.Content, "[redacted]")
	assert.Equal(t, ConnectionID, res.ConnectionID)
}

// The credential refuses a manifest that points somewhere other than the
// pinned host, even though the runtime would allow that host.
func TestEndToEnd_ManifestPointingElsewhereGetsNoToken(t *testing.T) {
	e := newEnv(t)
	var seen string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	src := e.source(APIHost) // pinned to api.github.com, not the test server

	m, a := githubManifest(srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	runner := httpaction.NewRunner(g).WithRootCAs(pool)

	_, err := runner.RunAuthenticated(context.Background(), m, a, map[string]any{}, src,
		httpaction.CallSite{RunID: e.newRun("idp|alice")})
	require.Error(t, err)
	assert.Empty(t, seen, "the token never reached a host other than api.github.com")
	assert.NotContains(t, err.Error(), "ghu_alice")
}

// githubManifest is the embedded `github` manifest with its base_url pointed
// at the test server: the real auth declaration, a different host.
func githubManifest(baseURL string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	a, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	if err != nil {
		panic(err)
	}
	m := proto.Clone(a.Manifest).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = baseURL
	for _, act := range m.GetActions() {
		if act.GetId() == a.Spec.GetId() {
			return m, act
		}
	}
	panic("user.get missing")
}
