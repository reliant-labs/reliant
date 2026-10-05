//go:build localmodel_e2e

// Copyright (c) 2025 Reliant Labs
package grpc

// Whole-chain proof for Wave 4 local models against a REAL local Ollama.
//
//	embedded NATS ── NATSToolBridge ── ToolsDaemonService ── NewDaemonServer (h2c, rlat_ auth)
//	                                          ▲                         ▲
//	  NATSDaemonRouter ◄─ DaemonRegistryService / CatalogService        │ ConnectDaemon
//	        ▲                                                  daemonruntime.Start ──► Ollama :11434
//	  NewLocalModelTransport ◄─ handlers.ProbeLLMCall (resolveLLMCall, local driver)
//
// Run:
//
//	DATABASE_URL=postgres://postgres:postgres@127.0.0.1:<port>/reliant?sslmode=disable \
//	  go test -tags localmodel_e2e -run LocalModelE2E -v ./internal/grpc -count=1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

const (
	e2eOllamaRoot  = "http://127.0.0.1:11434"
	e2eChatModel   = "qwen3:latest"
	e2eEmbedModel  = "nomic-embed-text:latest"
	e2eUserA       = "lm-e2e-user-a"
	e2eUserB       = "lm-e2e-user-b"
	e2eTestUserHdr = "X-Test-User"
)

func e2eStartNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second), "embedded NATS did not start")
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// e2eAPI serves DaemonRegistryService and CatalogService, with the caller's
// user id taken from the X-Test-User header (the real API derives it from the
// session JWT; that layer is not under test here).
func e2eAPI(t *testing.T, repo *db.Repo, router toolexec.DaemonRouter) (reliantv1connect.DaemonRegistryServiceClient, reliantv1connect.CatalogServiceClient) {
	t.Helper()
	asUser := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if uid := req.Header().Get(e2eTestUserHdr); uid != "" {
				ctx = context.WithValue(ctx, auth.UserIDContextKey, uid)
			}
			return next(ctx, req)
		}
	}))
	mux := http.NewServeMux()
	p1, h1 := reliantv1connect.NewDaemonRegistryServiceHandler(services.NewDaemonRegistryService(repo, router), asUser)
	mux.Handle(p1, h1)
	p2, h2 := reliantv1connect.NewCatalogServiceHandler(
		services.NewCatalogService(nil).WithLocalModels(local.NewRepoDirectory(repo)), asUser)
	mux.Handle(p2, h2)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reliantv1connect.NewDaemonRegistryServiceClient(http.DefaultClient, srv.URL),
		reliantv1connect.NewCatalogServiceClient(http.DefaultClient, srv.URL)
}

func asUser[T any](userID string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set(e2eTestUserHdr, userID)
	return r
}

func e2eHTTPJSON(t *testing.T, method, url string, into any) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, into), string(raw))
}

func findModel(inv *reliantv1.LocalModelInventory, name string) *reliantv1.LocalModelInfo {
	for _, ep := range inv.GetEndpoints() {
		for _, m := range ep.GetModels() {
			if m.GetName() == name {
				return m
			}
		}
	}
	return nil
}

func TestLocalModelE2E(t *testing.T) {
	// Ollama must be up; this is the point of the test.
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	e2eHTTPJSON(t, http.MethodGet, e2eOllamaRoot+"/api/tags", &tags)
	var haveChat bool
	for _, m := range tags.Models {
		haveChat = haveChat || m.Name == e2eChatModel
	}
	require.True(t, haveChat, "ollama at %s lacks %s: %+v", e2eOllamaRoot, e2eChatModel, tags.Models)

	// Never touch ~/.reliant.
	cfgDir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", cfgDir)
	t.Setenv("HOME", t.TempDir())

	repo, cleanupDB := db.SetupTestDB(t)
	t.Cleanup(cleanupDB)
	nc := e2eStartNATS(t)

	// ── gateway: real ToolsDaemonService + NATS bridge + daemon server ──
	authority := tokenauthority.NewMemory()
	toolsSvc := services.NewToolsDaemonService(repo)
	bridge := toolexec.NewNATSToolBridge(nc, nil, toolsSvc)
	toolsSvc.AddConnectionListener(bridge)
	require.NoError(t, bridge.Start())
	t.Cleanup(func() { _ = bridge.Close() })
	gw := NewDaemonServer(&DaemonConfig{ToolsDaemonService: toolsSvc, DaemonTokens: authority})
	gatewayURL := serveCleartext(t, gw.server.Handler)

	mint := func(userID string) string {
		m, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
			UserID: userID, Name: "e2e", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
		})
		require.NoError(t, err)
		return m.Plaintext
	}

	// ── worker/API side: the real router ──
	router := toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(repo))
	registry, catalog := e2eAPI(t, repo, router)

	// ── the real daemon runtime, dialing the gateway with a minted rlat_ ──
	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- daemonruntime.Start(daemonCtx, daemonruntime.StartOptions{BootstrapConfig: bootstrap.DaemonBootstrapConfig{
			AuthToken: mint(e2eUserA),
			GRPCURL:   gatewayURL,
			TLSMode:   bootstrap.TLSModeH2C,
			DataDir:   t.TempDir(),
			Name:      "e2e-laptop",
			ServerURL: "http://e2e.invalid",
		}})
	}()
	t.Cleanup(func() {
		stopDaemon()
		select {
		case <-daemonDone:
		case <-time.After(15 * time.Second):
		}
	})

	var daemonID string
	var inv *reliantv1.LocalModelInventory

	t.Run("a_inventory_arrives_and_is_stored", func(t *testing.T) {
		require.Eventually(t, func() bool {
			ds, err := registry.ListDaemons(context.Background(), asUser(e2eUserA, &reliantv1.ListDaemonsRequest{}))
			if err != nil || len(ds.Msg.GetDaemons()) == 0 {
				return false
			}
			d := ds.Msg.GetDaemons()[0]
			if d.GetLocalModels() == nil || findModel(d.GetLocalModels(), e2eChatModel) == nil {
				return false
			}
			daemonID, inv = d.GetDaemonId(), d.GetLocalModels()
			return true
		}, 60*time.Second, 500*time.Millisecond, "daemon never published an inventory containing %s", e2eChatModel)

		// stored in daemons.local_models (raw column through the repo API)
		stored, err := repo.ListDaemonLocalModels(context.Background(), e2eUserA)
		require.NoError(t, err)
		require.Contains(t, stored, daemonID)
		t.Logf("daemons.local_models[%s] = %.400s", daemonID, stored[daemonID])

		got, err := registry.GetDaemon(context.Background(), asUser(e2eUserA, &reliantv1.GetDaemonRequest{DaemonId: daemonID}))
		require.NoError(t, err)
		qwen := findModel(got.Msg.GetDaemon().GetLocalModels(), e2eChatModel)
		require.NotNil(t, qwen, "GetDaemon must carry qwen3")
		assert.True(t, qwen.GetSupportsChat())
		assert.True(t, qwen.GetSupportsTools())
		assert.True(t, qwen.GetSupportsThinking())
		assert.Greater(t, qwen.GetContextWindow(), int64(0))
		t.Logf("qwen3: %v", qwen)

		embed := findModel(got.Msg.GetDaemon().GetLocalModels(), e2eEmbedModel)
		require.NotNil(t, embed, "embedding model should be listed in the inventory (just not as chat)")
		assert.False(t, embed.GetSupportsChat(), "nomic-embed-text must not be chat-capable")
		t.Logf("nomic-embed-text: %v", embed)
	})
	if t.Failed() {
		t.FailNow()
	}

	var endpointID string
	t.Run("b_catalog_lists_local_model", func(t *testing.T) {
		resp, err := catalog.ListModels(context.Background(), asUser(e2eUserA, &reliantv1.ListModelsRequest{}))
		require.NoError(t, err)
		var found *reliantv1.ModelInfo
		for _, m := range resp.Msg.GetModels() {
			if m.GetId() == e2eChatModel+"@local" {
				found = m
			}
			assert.NotEqual(t, e2eEmbedModel+"@local", m.GetId(), "embedding model must not be in the catalog")
		}
		require.NotNil(t, found, "catalog lacks %s@local", e2eChatModel)
		require.NotNil(t, found.GetLocal())
		assert.True(t, found.GetLocal().GetOnline())
		assert.Equal(t, daemonID, found.GetLocal().GetDaemonId())
		assert.NotEmpty(t, found.GetLocal().GetMachineName())
		assert.NotEmpty(t, found.GetLocal().GetEndpointId())
		endpointID = found.GetLocal().GetEndpointId()
		t.Logf("catalog entry: id=%s machine=%q endpoint=%s kind=%s online=%v ctx=%d tools=%v reason=%v",
			found.GetId(), found.GetLocal().GetMachineName(), endpointID, found.GetLocal().GetEndpointKind(),
			found.GetLocal().GetOnline(), found.GetContextWindow(), found.GetSupportsTools(), found.GetCanReason())
		for _, ep := range inv.GetEndpoints() {
			t.Logf("inventory endpoint: id=%s kind=%s url=%s source=%s", ep.GetId(), ep.GetKind(), ep.GetBaseUrl(), ep.GetSource())
		}
	})

	localSpec := &handlers.LocalModelSpec{
		Directory: local.NewRepoDirectory(repo),
		Transport: func(userID, daemon, endpoint string) http.RoundTripper {
			return toolexec.NewLocalModelTransport(router, userID, daemon, endpoint)
		},
	}
	probe := func(userID string, ps handlers.ProbeSpec) handlers.ProbeResult {
		ps.UserID, ps.Local = userID, localSpec
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		return handlers.ProbeLLMCall(ctx, ps)
	}

	t.Run("c_worker_stream_PONG_thinking_usage", func(t *testing.T) {
		r := probe(e2eUserA, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local",
			History: []message.Message{handlers.ProbeUserMessage("Reply with exactly: PONG")},
		})
		require.NoError(t, r.Err())
		t.Logf("resolved=%s driver=%s text=%q thinkingLen=%d thinkingEvents=%d usage=%+v finish=%v latency=%s events=%v",
			r.ResolvedModelID, r.ProviderDriver, r.Text, len(r.Thinking), r.ThinkingEvents, r.Usage, r.FinishReason, r.Latency, r.EventTypes)
		assert.Contains(t, strings.ToUpper(r.Text), "PONG")
		assert.NotContains(t, r.Text, "<think>")
		assert.NotContains(t, r.Text, "</think>")
		assert.NotEmpty(t, r.Thinking, "qwen3 thinks; thinking must be captured separately")
		assert.Greater(t, r.Usage.InputTokens, int64(0))
		assert.Greater(t, r.Usage.OutputTokens, int64(0))

		// context_window must be the context Ollama really honors. Nothing in
		// the chain sends num_ctx — Ollama's /v1 endpoint ignores it — so the
		// loaded context is the server's own setting, and /api/ps reporting it
		// equal to the published inventory proves the worker compacts against
		// the real window rather than one Ollama silently truncates.
		var ps struct {
			Models []struct {
				Name          string `json:"name"`
				ContextLength int64  `json:"context_length"`
			} `json:"models"`
		}
		e2eHTTPJSON(t, http.MethodGet, e2eOllamaRoot+"/api/ps", &ps)
		var loaded int64
		for _, m := range ps.Models {
			if m.Name == e2eChatModel {
				loaded = m.ContextLength
			}
		}
		qwen := findModel(inv, e2eChatModel)
		t.Logf("/api/ps context_length=%d, inventory context_window=%d", loaded, qwen.GetContextWindow())
		assert.Equal(t, qwen.GetContextWindow(), loaded, "inventory context_window must equal the context Ollama actually loaded")
	})

	t.Run("d_tool_round_trip", func(t *testing.T) {
		ps := handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local",
			Tools:   []tools.Tool{handlers.ProbeSecretWordTool{}},
			History: []message.Message{handlers.ProbeUserMessage("Call the get_secret_word tool, then tell me the secret word it returned.")},
		}
		first := probe(e2eUserA, ps)
		require.NoError(t, first.Err())
		require.NotEmpty(t, first.ToolCalls, "turn 1 must call the tool (text=%q)", first.Text)
		t.Logf("turn1 tool call: %s %s", first.ToolCalls[0].Name, first.ToolCalls[0].Input)
		ps.History = append(ps.History, handlers.ProbeAssistantTurn(first), handlers.ProbeToolResultMessage(first.ToolCalls, "pineapple"))
		second := probe(e2eUserA, ps)
		require.NoError(t, second.Err())
		t.Logf("turn2 text=%q", second.Text)
		assert.Contains(t, strings.ToLower(second.Text), "pineapple")
	})

	t.Run("e_refresh_rpc", func(t *testing.T) {
		before := inv.GetProbedAt()
		time.Sleep(1100 * time.Millisecond) // probed_at has 1s resolution
		resp, err := registry.RefreshLocalModels(context.Background(), asUser(e2eUserA, &reliantv1.RefreshLocalModelsRequest{DaemonId: daemonID}))
		require.NoError(t, err)
		got := resp.Msg.GetLocalModels()
		require.NotNil(t, got)
		assert.NotNil(t, findModel(got, e2eChatModel))
		t.Logf("probed_at before=%s after=%s", before, got.GetProbedAt())
		assert.NotEqual(t, before, got.GetProbedAt(), "refresh must re-probe, not replay the cached inventory")
	})

	t.Run("f_set_endpoints_writes_config", func(t *testing.T) {
		const url = "http://127.0.0.1:11434/v1"
		resp, err := registry.SetLocalModelEndpoints(context.Background(), asUser(e2eUserA,
			&reliantv1.SetLocalModelEndpointsRequest{DaemonId: daemonID, BaseUrls: []string{url}}))
		require.NoError(t, err)
		cfg, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
		require.NoError(t, err, "config.yaml must be written under RELIANT_USER_CONFIG_DIR")
		t.Logf("config.yaml:\n%s", cfg)
		assert.Contains(t, string(cfg), url)
		var configured bool
		for _, ep := range resp.Msg.GetLocalModels().GetEndpoints() {
			t.Logf("after set: endpoint id=%s source=%s url=%s models=%d err=%q", ep.GetId(), ep.GetSource(), ep.GetBaseUrl(), len(ep.GetModels()), ep.GetError())
			configured = configured || ep.GetSource() == "configured"
		}
		assert.True(t, configured, "returned inventory must include a configured endpoint")

		resp, err = registry.SetLocalModelEndpoints(context.Background(), asUser(e2eUserA,
			&reliantv1.SetLocalModelEndpointsRequest{DaemonId: daemonID, BaseUrls: []string{}}))
		require.NoError(t, err)
		cfg, err = os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
		require.NoError(t, err)
		t.Logf("config.yaml after clear:\n%s", cfg)
		assert.NotContains(t, string(cfg), url)
		for _, ep := range resp.Msg.GetLocalModels().GetEndpoints() {
			assert.NotEqual(t, "configured", ep.GetSource(), "cleared: no configured endpoint may remain (%s)", ep.GetId())
		}
		// auto-detect still finds Ollama
		assert.NotNil(t, findModel(resp.Msg.GetLocalModels(), e2eChatModel))
	})

	t.Run("h_isolation_second_user", func(t *testing.T) {
		// B has a credential and no daemon.
		ds, err := registry.ListDaemons(context.Background(), asUser(e2eUserB, &reliantv1.ListDaemonsRequest{}))
		require.NoError(t, err)
		assert.Empty(t, ds.Msg.GetDaemons(), "B must not see A's daemons")

		_, err = registry.GetDaemon(context.Background(), asUser(e2eUserB, &reliantv1.GetDaemonRequest{DaemonId: daemonID}))
		require.Error(t, err)
		t.Logf("B GetDaemon(A's): %v", err)

		_, err = registry.RefreshLocalModels(context.Background(), asUser(e2eUserB, &reliantv1.RefreshLocalModelsRequest{DaemonId: daemonID}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		_, err = registry.SetLocalModelEndpoints(context.Background(), asUser(e2eUserB,
			&reliantv1.SetLocalModelEndpointsRequest{DaemonId: daemonID, BaseUrls: []string{"http://evil.invalid/v1"}}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

		resp, err := catalog.ListModels(context.Background(), asUser(e2eUserB, &reliantv1.ListModelsRequest{}))
		require.NoError(t, err)
		for _, m := range resp.Msg.GetModels() {
			assert.Nil(t, m.GetLocal(), "B's catalog must have no local models, saw %s", m.GetId())
		}

		// Resolve as B: both unpinned and pinned to A's daemon.
		r := probe(e2eUserB, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local",
			History: []message.Message{handlers.ProbeUserMessage("Reply with exactly: PONG")},
		})
		require.Error(t, r.Err(), "B must not resolve A's model")
		t.Logf("B resolve: %v", r.Err())
		all, err := local.ListModels(context.Background(), localSpec.Directory, e2eUserB)
		require.NoError(t, err)
		_, err = local.Resolve(all, models.ModelSelector{ID: e2eChatModel + "@local", Providers: []string{local.ProviderPrefix + daemonID}}, "")
		require.Error(t, err, "B pinning A's daemon id must not resolve")
		t.Logf("B pinned resolve: %v", err)

		// Relay directly as B to A's daemon: NATS subjects are user-scoped, so
		// nothing is listening.
		rt := toolexec.NewLocalModelTransport(router, e2eUserB, daemonID, endpointID)
		req, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
		start := time.Now()
		resp2, err := rt.RoundTrip(req)
		if resp2 != nil {
			resp2.Body.Close()
		}
		require.Error(t, err, "B must not relay to A's daemon")
		t.Logf("B relay to A's daemon: %v (%s)", err, time.Since(start))
		assert.Less(t, time.Since(start), 5*time.Second)

		// Sanity: A still can.
		rtA := toolexec.NewLocalModelTransport(router, e2eUserA, daemonID, endpointID)
		reqA, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
		respA, err := rtA.RoundTrip(reqA)
		require.NoError(t, err)
		body, _ := io.ReadAll(respA.Body)
		respA.Body.Close()
		assert.Equal(t, 200, respA.StatusCode)
		assert.Contains(t, string(body), e2eChatModel)
	})

	t.Run("g_disconnect_offline", func(t *testing.T) {
		stopDaemon()
		select {
		case <-daemonDone:
		case <-time.After(20 * time.Second):
			t.Fatal("daemon runtime did not stop")
		}
		require.Eventually(t, func() bool {
			resp, err := catalog.ListModels(context.Background(), asUser(e2eUserA, &reliantv1.ListModelsRequest{}))
			if err != nil {
				return false
			}
			for _, m := range resp.Msg.GetModels() {
				if m.GetId() == e2eChatModel+"@local" {
					t.Logf("catalog after disconnect: online=%v machine=%q", m.GetLocal().GetOnline(), m.GetLocal().GetMachineName())
					return !m.GetLocal().GetOnline()
				}
			}
			return false
		}, 30*time.Second, 500*time.Millisecond, "model must stay listed with online=false after disconnect")

		start := time.Now()
		r := probe(e2eUserA, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local",
			History: []message.Message{handlers.ProbeUserMessage("Reply with exactly: PONG")},
		})
		elapsed := time.Since(start)
		require.Error(t, r.Err())
		t.Logf("offline resolve (%s): %v", elapsed, r.Err())
		var unavailable *local.UnavailableError
		require.ErrorAs(t, r.Err(), &unavailable)
		assert.Contains(t, r.Err().Error(), "offline")
		assert.Less(t, elapsed, 3*time.Second)
	})
}
