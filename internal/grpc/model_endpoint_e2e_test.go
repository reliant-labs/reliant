//go:build localmodel_e2e

// Copyright (c) 2025 Reliant Labs
package grpc

// Whole-chain proof for custom model endpoints against a REAL Ollama, reached
// both ways. Reuses localmodel_e2e_test.go's helpers.
//
//	ModelEndpointService (VIA_DAEMON) ─► NATSDaemonRouter ─► NATS ─► gateway ─► real daemon ─► Ollama :11434
//	     │  SetLocalModelEndpoints-equivalent authorizes the endpoint on the daemon's relay
//	     └─ catalog / resolve / ProbeLLMCall ("<model>@local", provider endpoint:<id>)
//
// Run:
//
//	DATABASE_URL=postgres://postgres:postgres@127.0.0.1:<port>/reliant?sslmode=disable \
//	  go test -tags localmodel_e2e -run ModelEndpointE2E -v ./internal/grpc -count=1

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

func epCtx(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

func TestModelEndpointE2E(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "") // self-host: loopback is a legitimate DIRECT target
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	e2eHTTPJSON(t, http.MethodGet, e2eOllamaRoot+"/api/tags", &tags)
	require.NotEmpty(t, tags.Models, "ollama must be running with %s", e2eChatModel)

	t.Setenv("RELIANT_USER_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	repo, cleanupDB := db.SetupTestDB(t)
	t.Cleanup(cleanupDB)
	nc := e2eStartNATS(t)

	authority := tokenauthority.NewMemory()
	toolsSvc := services.NewToolsDaemonService(repo)
	bridge := toolexec.NewNATSToolBridge(nc, nil, toolsSvc)
	toolsSvc.AddConnectionListener(bridge)
	require.NoError(t, bridge.Start())
	t.Cleanup(func() { _ = bridge.Close() })
	gw := NewDaemonServer(&DaemonConfig{ToolsDaemonService: toolsSvc, DaemonTokens: authority})
	gatewayURL := serveCleartext(t, gw.server.Handler)

	m, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: e2eUserA, Name: "e2e", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
	})
	require.NoError(t, err)

	router := toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(repo))
	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- daemonruntime.Start(daemonCtx, daemonruntime.StartOptions{BootstrapConfig: bootstrap.DaemonBootstrapConfig{
			AuthToken: m.Plaintext, GRPCURL: gatewayURL, TLSMode: bootstrap.TLSModeH2C,
			DataDir: t.TempDir(), Name: "e2e-laptop", ServerURL: "http://e2e.invalid",
		}})
	}()
	t.Cleanup(func() {
		stopDaemon()
		select {
		case <-daemonDone:
		case <-time.After(15 * time.Second):
		}
	})

	registry, catalogClient := e2eAPI(t, repo, router)
	var daemonID string
	require.Eventually(t, func() bool {
		ds, err := registry.ListDaemons(context.Background(), asUser(e2eUserA, &reliantv1.ListDaemonsRequest{}))
		if err != nil || len(ds.Msg.GetDaemons()) == 0 || ds.Msg.GetDaemons()[0].GetLocalModels() == nil {
			return false
		}
		daemonID = ds.Msg.GetDaemons()[0].GetDaemonId()
		return true
	}, 60*time.Second, 500*time.Millisecond, "daemon never published an inventory")

	endpointSvc := services.NewModelEndpointService(repo, router, nil)

	// ── 1. VIA_DAEMON ─────────────────────────────────────────────────────
	// Use a base URL the daemon's AUTO-DETECTION would not produce
	// ("127.0.0.1", not "localhost"), so the relay can only authorize it
	// because the endpoint service configured it.
	const viaURL = "http://127.0.0.1:11434/v1"
	var viaID string
	t.Run("a_create_via_daemon_authorizes_relay_and_probes_through_it", func(t *testing.T) {
		resp, err := endpointSvc.CreateModelEndpoint(epCtx(e2eUserA), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{
				Name: "Laptop Ollama", BaseUrl: viaURL,
				Route:    reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON,
				DaemonId: daemonID,
				Models:   []*reliantv1.ModelEndpointModel{{Name: e2eChatModel, Temperature: ptrF(0.2), ExtraBodyJson: `{"seed": 7}`}},
			},
		}))
		require.NoError(t, err)
		ep := resp.Msg.GetEndpoint()
		viaID = ep.GetId()
		require.Empty(t, ep.GetProbe().GetError(), "probe through the real relay: %v", ep.GetProbe())
		assert.Equal(t, "ollama", ep.GetProbe().GetKind(), "the daemon's own prober detected the server family")

		var qwen *reliantv1.LocalModelInfo
		for _, mm := range ep.GetProbe().GetModels() {
			if mm.GetName() == e2eChatModel {
				qwen = mm
			}
		}
		require.NotNil(t, qwen, "probe lists %s: %v", e2eChatModel, ep.GetProbe().GetModels())
		assert.True(t, qwen.GetSupportsTools())
		assert.Greater(t, qwen.GetContextWindow(), int64(0))
		t.Logf("probed through relay: kind=%s qwen3 ctx=%d tools=%v thinking=%v", ep.GetProbe().GetKind(), qwen.GetContextWindow(), qwen.GetSupportsTools(), qwen.GetSupportsThinking())

		ds, err := registry.GetDaemon(context.Background(), asUser(e2eUserA, &reliantv1.GetDaemonRequest{DaemonId: daemonID}))
		require.NoError(t, err)
		var configured []string
		for _, e := range ds.Msg.GetDaemon().GetLocalModels().GetEndpoints() {
			if e.GetSource() == "configured" {
				configured = append(configured, e.GetBaseUrl())
			}
		}
		assert.Equal(t, []string{"http://127.0.0.1:11434/v1"}, configured, "the daemon now holds the endpoint in its relay allow-list")
	})
	if t.Failed() {
		t.FailNow()
	}

	spec := &handlers.LocalModelSpec{
		Directory: local.NewRepoDirectory(repo),
		Transport: func(userID, daemon, endpoint string) http.RoundTripper {
			return toolexec.NewLocalModelTransport(router, userID, daemon, endpoint)
		},
		Custom: &local.CustomRoutes{
			Relay: func(userID, daemon, endpoint string) http.RoundTripper {
				return toolexec.NewLocalModelTransport(router, userID, daemon, endpoint)
			},
			Policy: netguard.Policy{AllowPrivate: true},
		},
	}
	probe := func(userID string, ps handlers.ProbeSpec) handlers.ProbeResult {
		ps.UserID, ps.Local = userID, spec
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		return handlers.ProbeLLMCall(ctx, ps)
	}
	t.Run("b_catalog_groups_the_endpoint_under_its_name", func(t *testing.T) {
		resp, err := catalogClient.ListModels(context.Background(), asUser(e2eUserA, &reliantv1.ListModelsRequest{}))
		require.NoError(t, err)
		var found *reliantv1.ModelInfo
		for _, info := range resp.Msg.GetModels() {
			if info.GetLocal().GetEndpointId() == viaID && info.GetId() == e2eChatModel+"@local" {
				found = info
			}
		}
		require.NotNil(t, found, "catalog lacks the configured endpoint's model")
		assert.Equal(t, "Laptop Ollama", found.GetLocal().GetMachineName())
		assert.True(t, found.GetLocal().GetOnline())
	})

	t.Run("c_chat_completion_through_the_relay_for_a_configured_endpoint", func(t *testing.T) {
		r := probe(e2eUserA, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local", Providers: []string{"endpoint:" + viaID},
			History: []message.Message{handlers.ProbeUserMessage("Reply with exactly: PONG")},
		})
		require.NoError(t, r.Err())
		t.Logf("resolved=%s driver=%s text=%q usage=%+v latency=%s", r.ResolvedModelID, r.ProviderDriver, r.Text, r.Usage, r.Latency)
		assert.Contains(t, strings.ToUpper(r.Text), "PONG")
		assert.Greater(t, r.Usage.OutputTokens, int64(0))
	})

	t.Run("d_another_user_cannot_use_or_see_it", func(t *testing.T) {
		list, err := endpointSvc.ListModelEndpoints(epCtx(e2eUserB), connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
		require.NoError(t, err)
		assert.Empty(t, list.Msg.GetEndpoints())
		r := probe(e2eUserB, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local", Providers: []string{"endpoint:" + viaID},
			History: []message.Message{handlers.ProbeUserMessage("hi")},
		})
		require.Error(t, r.Err())
		t.Logf("B resolve: %v", r.Err())
	})

	// ── 2. DIRECT ─────────────────────────────────────────────────────────
	t.Run("e_direct_endpoint_hits_ollama_from_the_server_side", func(t *testing.T) {
		resp, err := endpointSvc.CreateModelEndpoint(epCtx(e2eUserA), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{
				Name: "Direct Ollama", BaseUrl: e2eOllamaRoot + "/v1",
				Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT,
			},
		}))
		require.NoError(t, err)
		ep := resp.Msg.GetEndpoint()
		require.Empty(t, ep.GetProbe().GetError(), "%v", ep.GetProbe())
		assert.Equal(t, "ollama", ep.GetProbe().GetKind())

		r := probe(e2eUserA, handlers.ProbeSpec{
			ModelID: e2eChatModel + "@local", Providers: []string{"endpoint:" + ep.GetId()},
			History: []message.Message{handlers.ProbeUserMessage("Reply with exactly: PONG")},
		})
		require.NoError(t, r.Err())
		t.Logf("direct: text=%q usage=%+v", r.Text, r.Usage)
		assert.Contains(t, strings.ToUpper(r.Text), "PONG")
	})

	t.Run("f_hosted_policy_refuses_the_same_direct_endpoint", func(t *testing.T) {
		t.Setenv("RELIANT_CONTROL_PLANE_URL", "http://admin.cluster.local:8090")
		hosted := services.NewModelEndpointService(repo, router, nil)
		_, err := hosted.CreateModelEndpoint(epCtx(e2eUserA), connect.NewRequest(&reliantv1.CreateModelEndpointRequest{
			Endpoint: &reliantv1.ModelEndpointInput{Name: "Hosted Direct", BaseUrl: e2eOllamaRoot + "/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	// ── 3. delete revokes the relay authorization ────────────────────────
	t.Run("g_delete_revokes_the_endpoint_from_the_daemon", func(t *testing.T) {
		_, err := endpointSvc.DeleteModelEndpoint(epCtx(e2eUserA), connect.NewRequest(&reliantv1.DeleteModelEndpointRequest{Id: viaID}))
		require.NoError(t, err)
		// The stored inventory is written just after the refresh reply is
		// delivered, so wait for it rather than reading once.
		require.Eventually(t, func() bool {
			ds, err := registry.GetDaemon(context.Background(), asUser(e2eUserA, &reliantv1.GetDaemonRequest{DaemonId: daemonID}))
			if err != nil {
				return false
			}
			for _, e := range ds.Msg.GetDaemon().GetLocalModels().GetEndpoints() {
				if e.GetSource() == "configured" {
					return false
				}
			}
			return true
		}, 10*time.Second, 100*time.Millisecond, "the daemon must no longer hold the deleted endpoint")
	})
}

func ptrF(v float64) *float64 { return &v }
