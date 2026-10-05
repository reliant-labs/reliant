// Copyright (c) 2025 Reliant Labs

package connauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const canary = "sk-canary-7f3a9c1e5d2b8046"

type env struct {
	t        *testing.T
	repo     *db.Repo
	svc      *connections.Service
	resolver *connections.Resolver
	source   *connauth.Source
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
	broker := connections.NewBroker(store, v, providers, nil, "https://reliant.example")
	resolver := connections.NewResolver(repo, store, tokens)
	return &env{t: t, repo: repo, svc: connections.NewService(store, v, providers, tokens, broker, nil), resolver: resolver, source: connauth.New(resolver)}
}

func (e *env) newRun(userID string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID := uuid.NewString()
	require.NoError(e.t, e.repo.CreateChat(ctx, &db.Chat{ID: chatID, Title: "t", ProjectID: "test-project", UserID: userID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := e.repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(e.t, err)
	owner := userID
	require.NoError(e.t, e.repo.CreateWorkflow(ctx, &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID, Status: db.Active(), CreatedAt: now, OwnerUserID: &owner}))
	return chatID
}

func (e *env) apiKey(userID, integration, name, header string) string {
	e.t.Helper()
	fields := map[string]string{"api_key": canary}
	if header != "" {
		fields["header"] = header
	}
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: integration, Name: name, Kind: connections.APIKeyKindAPIKey, Fields: fields,
	})
	require.NoError(e.t, err)
	return conn.ID
}

// githubPAT saves a GitHub personal access token: a credential for a different
// integration than http, whose manifest pins it to api.github.com.
func (e *env) githubPAT(userID string) string {
	e.t.Helper()
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: "github", Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": canary},
	})
	require.NoError(e.t, err)
	return conn.ID
}

func (e *env) basic(userID, integration, name string) string {
	e.t.Helper()
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: integration, Name: name, Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"username": "svc", "password": canary},
	})
	require.NoError(e.t, err)
	return conn.ID
}

func localRunner() *httpaction.Runner {
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g)
}

func trusting(srv *httptest.Server) *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return localRunner().WithRootCAs(pool)
}

func httpRequest(t *testing.T) (*catalog.Action, map[string]any) {
	a, err := catalog.MustBuiltin().Resolve("http/request@1")
	require.NoError(t, err)
	return a, map[string]any{}
}

// serve returns a TLS server, a runner that trusts it, and what the server saw.
func serve(t *testing.T, echo bool) (srvURL string, runner *httpaction.Runner, seen *http.Header) {
	t.Helper()
	got := http.Header{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		if echo {
			// A hostile or sloppy upstream that reflects the credential back.
			_, _ = w.Write([]byte(`{"you_sent":"` + r.Header.Get("Authorization") + r.Header.Get("X-Api-Key") + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, trusting(srv), &got
}

func TestAPIKeyConnectionSendsTheHeader(t *testing.T) {
	e := newEnv(t)
	u, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	run := e.newRun("alice")

	params["url"] = u + "/x"
	params["connection"] = e.apiKey("alice", "http", "k", "x-api-key")
	res, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: run, NodeID: "n1"})
	require.NoError(t, err)
	assert.Equal(t, canary, seen.Get("X-Api-Key"), "the api_key connection's header reaches the server")
	assert.Equal(t, params["connection"], res.ConnectionID, "the connection id is recorded for audit")
	assert.False(t, res.IsError)

	// Default header choice is a bearer token.
	params["connection"] = e.apiKey("alice", "http", "k2", "")
	_, err = runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: run})
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+canary, seen.Get("Authorization"))
}

func TestBasicConnectionSendsBasicAuth(t *testing.T) {
	e := newEnv(t)
	u, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = u
	params["connection"] = e.basic("alice", "http", "b")
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(seen.Get("Authorization"), "Basic "))
}

func TestNoConnectionBehavesAsBefore(t *testing.T) {
	e := newEnv(t)
	u, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = u
	res, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.NoError(t, err)
	assert.Empty(t, seen.Get("Authorization"))
	assert.Empty(t, res.ConnectionID)
}

func TestCanaryNeverAppearsInOutputErrorsOrLogs(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	u, runner, _ := serve(t, true)
	a, params := httpRequest(t)
	params["url"] = u
	params["connection"] = e.apiKey("alice", "http", "k", "x-api-key")
	res, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.NoError(t, err)

	encoded, _ := json.Marshal(res)
	assert.NotContains(t, string(encoded), canary, "an upstream that echoes the key must not leak it into the result")
	assert.NotContains(t, res.Content, canary)
	assert.Contains(t, res.Content, vault.Redacted)
	assert.NotContains(t, logs.String(), canary)

	// An error path: the credential is applied, then the call fails.
	params["url"] = "https://127.0.0.1:1/" + canary // refused or unreachable; the URL echoes the key
	_, err = runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), canary)
}

func TestAnotherUsersConnectionIsRefused(t *testing.T) {
	e := newEnv(t)
	u, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = u
	params["connection"] = e.apiKey("bob", "http", "bobs", "")
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.Error(t, err)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Empty(t, seen.Get("Authorization"), "no request may be made")
}

func TestMissingConnectionIsFailedPrecondition(t *testing.T) {
	e := newEnv(t)
	u, runner, _ := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = u
	params["connection"] = "conn_does_not_exist"
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.True(t, errors.Is(ce.Err, connections.ErrFailedPrecondition), "the typed connections error is preserved")
}

func TestConnectionForAnotherIntegrationIsRefused(t *testing.T) {
	e := newEnv(t)
	u, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = u
	params["connection"] = e.githubPAT("alice")
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.Error(t, err, "a token saved for github must not be sent through the http integration")
	assert.Empty(t, seen.Get("Authorization"))
}

func TestDaemonPlacedCallerIsRefused(t *testing.T) {
	e := newEnv(t)
	run := e.newRun("alice")
	id := e.apiKey("alice", "http", "k", "")
	_, err := e.source.Credential(context.Background(), httpaction.CredentialRequest{RunID: run, ConnectionID: id, IntegrationID: "http", ServerPlaced: false})
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.True(t, errors.Is(err, connections.ErrDaemonPlacement))
}

func TestCredentialNeverFollowsARedirectToAnotherHost(t *testing.T) {
	e := newEnv(t)
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("credential reached a second host: %v", r.Header)
	}))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	runner := trusting(srv)
	a, params := httpRequest(t)
	params["url"] = srv.URL
	params["connection"] = e.apiKey("alice", "http", "k", "")
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, e.source, httpaction.CallSite{RunID: e.newRun("alice")})
	require.Error(t, err)
}

func TestNilCredentialSourceRefusesInsteadOfRunningUnauthenticated(t *testing.T) {
	_, runner, seen := serve(t, false)
	a, params := httpRequest(t)
	params["url"] = "https://example.com"
	params["connection"] = "conn_x"
	_, err := runner.RunAuthenticated(context.Background(), a.Manifest, a.Spec, params, nil, httpaction.CallSite{RunID: "r"})
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Empty(t, seen.Get("Authorization"))
}

func (e *env) trigger(userID, integration string, connID *string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "proj-" + uuid.NewString()
	require.NoError(e.t, e.repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "p", Path: e.t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: integration, Events: []string{"x"}})
	require.NoError(e.t, err)
	id := "trg-" + uuid.NewString()
	require.NoError(e.t, e.repo.CreateTrigger(ctx, &core.Trigger{
		ID: id, UserID: userID, ProjectID: projectID, Name: id, Kind: core.TriggerKindIntegration, Enabled: true,
		Workflow: "builtin://agent", DaemonID: "d", Config: cfg, ConnectionID: connID, CreatedAt: now, UpdatedAt: now,
	}))
	return id
}

// A poll resolves its credential by trigger id alone; the owner and the
// connection come from the trigger row. The credential it gets is the
// owner's, pinned to the integration's host and scrubbing itself.
func TestForTriggerResolvesFromTheTriggerRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pat := e.githubPAT("alice")
	trigger := e.trigger("alice", "github", &pat)

	cred, err := e.source.ForTrigger(ctx, trigger)
	require.NoError(t, err)
	assert.Equal(t, pat, cred.ConnectionID())
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, cred.Apply(req))
	assert.Equal(t, "Bearer "+canary, req.Header.Get("Authorization"))
	assert.NotContains(t, cred.Scrub("echo "+canary), canary)

	off, _ := http.NewRequest(http.MethodGet, "https://attacker.example/collect", nil)
	require.Error(t, cred.Apply(off), "a poller that builds a URL to another host gets nothing")
	assert.Empty(t, off.Header.Get("Authorization"))
}

func TestForTriggerRefusesAnotherUsersConnection(t *testing.T) {
	e := newEnv(t)
	alices := e.githubPAT("alice")
	bobsTrigger := e.trigger("bob", "github", &alices)

	_, err := e.source.ForTrigger(context.Background(), bobsTrigger)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.NotContains(t, err.Error(), alices, "the refusal does not confirm the foreign id exists")
}

func TestForTriggerNeedsReauthIsTyped(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	require.NoError(t, e.repo.Connections().WithSecretsLock(context.Background(), "alice", pat, func(tx core.SecretsTx) error {
		return tx.MarkStatus(context.Background(), core.ConnectionStatusNeedsReauth, "invalid_grant")
	}))
	trigger := e.trigger("alice", "github", &pat)

	_, err := e.source.ForTrigger(context.Background(), trigger)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeNeedsReauth, ce.Code)
}

func TestForTriggerWithoutAResolverRefuses(t *testing.T) {
	_, err := connauth.New(nil).ForTrigger(context.Background(), "trg-1")
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
}

// The tool form resolves the connection for the run's owner, taken from the run
// the tool executes in, not from anything the model supplies.
func TestToolFormUsesTheRunOwner(t *testing.T) {
	e := newEnv(t)
	got := http.Header{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	restore := tools.UseIntegrationRunner(trusting(srv))
	defer restore()

	factory := tools.NewToolsFactory(&tools.ToolsOptions{IntegrationCredentials: e.source})
	tool := factory.GetToolByName("http__request", nil)
	require.NotNil(t, tool)

	aliceRun := e.newRun("alice")
	aliceKey := e.apiKey("alice", "http", "k", "")
	bobKey := e.apiKey("bob", "http", "bobs", "")

	call := func(run, conn string) tools.ToolResponse {
		input, _ := json.Marshal(map[string]any{"url": srv.URL, "connection": conn})
		resp, err := tool.Run(&rctx.ToolContext{Context: context.Background(), ChatID: run, Thread: run}, tools.ToolCall{ID: "tc1", Name: "http__request", Input: string(input)})
		require.NoError(t, err)
		return resp
	}
	ok := call(aliceRun, aliceKey)
	assert.False(t, ok.IsError, ok.Content)
	assert.Equal(t, "Bearer "+canary, got.Get("Authorization"))
	assert.NotContains(t, ok.Content+ok.Metadata, canary)
	assert.Contains(t, ok.Metadata, aliceKey, "connection id is recorded on the tool result")

	got = http.Header{}
	denied := call(aliceRun, bobKey)
	assert.True(t, denied.IsError, "another user's connection id is refused when alice's run calls the tool")
	assert.Empty(t, got.Get("Authorization"))
}
