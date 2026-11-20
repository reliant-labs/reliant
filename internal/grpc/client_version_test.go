// Copyright (c) 2026 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/reliant-labs/forge/pkg/svcerr"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The client version gate (clientVersionGate, refuseLegacyUnidentified):
// ClientVersionPolicy decides which Reliant-Client callers the API refuses.
// These drive the real listener and chain, as the api root wires them, with
// the policy on both.

const testBrowserOrigin = "https://app.reliantlabs.io"

// testSystem is the real SystemService: the policy's exemption.
func testSystem(t *testing.T) testRoute {
	t.Helper()
	svc, err := system.New(system.Deps{Logger: slog.Default(), Config: &config.Config{}})
	require.NoError(t, err)
	return mountService(reliantv1connect.SystemServiceName, svc)
}

// floorPolicy floors the CLI at 1.9.0 and names how to upgrade it.
func floorPolicy(enforce bool) *configv1.ClientVersionPolicy {
	return &configv1.ClientVersionPolicy{
		Cli: &configv1.CliClientVersionFloor{
			MinVersion:     "1.9.0",
			UpgradeUrl:     "https://reliantlabs.io/download",
			UpgradeCommand: "brew upgrade --cask reliant",
		},
		Enforce: enforce,
	}
}

// asClient is authed(msg) naming the client in Reliant-Client ("" sends none)
// and, when origin is set, a browser's Origin.
func asClient[T any](msg *T, client, origin string) *connect.Request[T] {
	req := authed(msg)
	if client != "" {
		req.Header().Set("Reliant-Client", client)
	}
	if origin != "" {
		req.Header().Set("Origin", origin)
	}
	return req
}

// reachedForgeHandler is what ForgeService.GetTopology answers an empty
// request once it runs: InvalidArgument, so the gate let the call through.
func requireReachedHandler(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "the call should reach the handler; got %v", err)
}

// requireUpgradeRequired asserts the refusal's wire shape: code, reason
// header, message and the ErrorInfo detail.
func requireUpgradeRequired(t *testing.T, err error, wantMessage string, wantMeta map[string]string) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "want a Connect error, got %v", err)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code(), "got %v", err)
	require.Equal(t, "upgrade_required", connectErr.Meta().Get(svcerr.ReasonHeader))
	require.Equal(t, wantMessage, connectErr.Message())

	var info *errdetails.ErrorInfo
	for _, d := range connectErr.Details() {
		v, derr := d.Value()
		require.NoError(t, derr)
		if ei, ok := v.(*errdetails.ErrorInfo); ok {
			info = ei
		}
	}
	require.NotNil(t, info, "the refusal carries a google.rpc.ErrorInfo detail")
	require.Equal(t, "UPGRADE_REQUIRED", info.GetReason())
	require.Equal(t, "reliantlabs.io", info.GetDomain())
	require.Equal(t, wantMeta, info.GetMetadata())
}

func TestClientVersionGateEnforcesFloors(t *testing.T) {
	_, baseURL := startTestServerWithClientVersions(t, floorPolicy(true), testForge(t), testSystem(t))
	forge := forgeClient(baseURL)

	t.Run("below the floor is refused upgrade_required", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "cli/1.8.9", ""))
		requireUpgradeRequired(t, err,
			"this cli (1.8.9) is older than the minimum this server supports (1.9.0); upgrade: brew upgrade --cask reliant",
			map[string]string{
				"client_kind":     "cli",
				"client_version":  "1.8.9",
				"min_version":     "1.9.0",
				"upgrade_url":     "https://reliantlabs.io/download",
				"upgrade_command": "brew upgrade --cask reliant",
			})
	})
	t.Run("a dev build of the floor's release is below it", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "cli/1.9.0-dev+abc123", ""))
		require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "got %v", err)
	})
	t.Run("at the floor passes", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "cli/1.9.0", ""))
		requireReachedHandler(t, err)
	})
	t.Run("above the floor passes", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "cli/1.10.0+abc", ""))
		requireReachedHandler(t, err)
	})
	t.Run("a kind with no floor passes", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "sdk-go/0.0.0-20261120010203-abcdef123456", ""))
		requireReachedHandler(t, err)
	})
	t.Run("an unparseable header passes", func(t *testing.T) {
		for _, v := range []string{"cli/latest", "toaster/1.0.0", "garbage"} {
			_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, v, ""))
			requireReachedHandler(t, err)
		}
	})
	t.Run("a missing header passes while refuse_unidentified is off", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "", ""))
		requireReachedHandler(t, err)
		_, err = forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "", testBrowserOrigin))
		requireReachedHandler(t, err)
	})
	t.Run("SystemService is exempt", func(t *testing.T) {
		sys := reliantv1connect.NewSystemServiceClient(h2cClient(), baseURL)
		_, err := sys.Version(context.Background(), asClient(&reliantv1.VersionRequest{}, "cli/0.1.0", ""))
		require.NoError(t, err)
	})
	t.Run("after authentication", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.GetForgeTopologyRequest{})
		req.Header().Set("Reliant-Client", "cli/1.0.0")
		_, err := forge.GetTopology(context.Background(), req)
		require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "got %v", err)
	})
}

func TestClientVersionGateObservesWithoutEnforce(t *testing.T) {
	_, baseURL := startTestServerWithClientVersions(t, floorPolicy(false), testForge(t))
	forge := forgeClient(baseURL)

	below := observability.ClientVersionTotal.WithLabelValues("cli", clientOutcomeBelow)
	before := testutil.ToFloat64(below)
	_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "cli/1.0.0", ""))
	requireReachedHandler(t, err)
	require.InDelta(t, before+1, testutil.ToFloat64(below), 0, "a below-floor call is counted when enforce is off")
}

func TestClientVersionGateRefusesStreams(t *testing.T) {
	policy := &configv1.ClientVersionPolicy{
		Web:     &configv1.WebClientVersionFloor{MinVersion: "1.9.0", UpgradeUrl: "https://app.reliantlabs.io"},
		Enforce: true,
	}
	_, baseURL := startTestServerWithClientVersions(t, policy, testStreaming(t))
	client := reliantv1connect.NewStreamingServiceClient(h2cClient(), baseURL)

	t.Run("below the floor", func(t *testing.T) {
		stream, err := client.StreamUserUpdates(context.Background(), asClient(&reliantv1.StreamUserUpdatesRequest{}, "web/1.8.0", ""))
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		require.False(t, stream.Receive(), "a refused stream sends nothing")
		requireUpgradeRequired(t, stream.Err(),
			"this web (1.8.0) is older than the minimum this server supports (1.9.0); upgrade: https://app.reliantlabs.io",
			map[string]string{
				"client_kind":     "web",
				"client_version":  "1.8.0",
				"min_version":     "1.9.0",
				"upgrade_url":     "https://app.reliantlabs.io",
				"upgrade_command": "",
			})
	})
	t.Run("at the floor", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := client.StreamUserUpdates(ctx, asClient(&reliantv1.StreamUserUpdatesRequest{}, "web/1.9.0", ""))
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()
		require.True(t, stream.Receive(), "stream ended before its sync event: %v", stream.Err())
	})
}

func TestRefuseUnidentified(t *testing.T) {
	// No floor at all: refuse_unidentified must not depend on one. An SDK
	// that names itself passes; one that does not is refused.
	policy := &configv1.ClientVersionPolicy{RefuseUnidentified: true}
	_, baseURL := startTestServerWithClientVersions(t, policy,
		testForge(t), testSystem(t), testHTTPRoutes(t, testUserAuth(t)))
	forge := forgeClient(baseURL)

	t.Run("a non-browser call with no header is refused upgrade_required", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "", ""))
		requireUpgradeRequired(t, err,
			"this client sends no Reliant-Client version; upgrade to the latest version",
			map[string]string{
				"client_kind": "", "client_version": "", "min_version": "", "upgrade_url": "", "upgrade_command": "",
			})
	})
	t.Run("a browser-origin call with no header gets the legacy 404", func(t *testing.T) {
		_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, "", testBrowserOrigin))
		var connectErr *connect.Error
		require.True(t, errors.As(err, &connectErr), "got %v", err)
		require.Equal(t, connect.CodeUnimplemented, connectErr.Code(), "got %v", err)
		require.Empty(t, connectErr.Meta().Get(svcerr.ReasonHeader), "a bare 404, not a Connect error")
	})
	t.Run("a call that names itself passes, with no floor configured", func(t *testing.T) {
		for _, v := range []string{"sdk-go/0.0.0-20261120010203-abcdef123456", "sdk-ts/0.1.0", "web/1.0.0"} {
			_, err := forge.GetTopology(context.Background(), asClient(&reliantv1.GetForgeTopologyRequest{}, v, testBrowserOrigin))
			requireReachedHandler(t, err)
		}
	})
	t.Run("SystemService is exempt, browser or not", func(t *testing.T) {
		sys := reliantv1connect.NewSystemServiceClient(h2cClient(), baseURL)
		_, err := sys.Version(context.Background(), connect.NewRequest(&reliantv1.VersionRequest{}))
		require.NoError(t, err)
		req := connect.NewRequest(&reliantv1.VersionRequest{})
		req.Header().Set("Origin", testBrowserOrigin)
		_, err = sys.Version(context.Background(), req)
		require.NoError(t, err)
	})
	t.Run("paths outside reliant.v1 are never refused", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
		require.NoError(t, err)
		req.Header.Set("Origin", testBrowserOrigin)
		resp, err := h2cClient().Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// The legacy lever alone: browser-origin calls with no header get the 404 a
// pre-header bundle turns into its out-of-date modal; everything else is
// served.
func TestEnforceLegacyUnidentified(t *testing.T) {
	policy := &configv1.ClientVersionPolicy{EnforceLegacyUnidentified: true}
	_, baseURL := startTestServerWithClientVersions(t, policy, testForge(t), testSystem(t))

	post := func(t *testing.T, path, origin, client string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader("{}"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+drainTestAPIKey)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if client != "" {
			req.Header.Set("Reliant-Client", client)
		}
		resp, err := h2cClient().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	const topology = "/reliant.v1.ForgeService/GetTopology"

	t.Run("browser origin, no header: a bare 404 the browser may read", func(t *testing.T) {
		resp := post(t, topology, testBrowserOrigin, "")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"code"`, "no Connect error body: connect-web reports it as \"HTTP 404\"")
		require.NotEmpty(t, resp.Header.Get("Access-Control-Allow-Origin"), "inside CORS, or the page sees a CORS failure instead")
	})
	t.Run("browser origin with the header is served", func(t *testing.T) {
		resp := post(t, topology, testBrowserOrigin, "web/1.0.0")
		require.NotEqual(t, http.StatusNotFound, resp.StatusCode)
	})
	t.Run("no origin, no header (CLI, SDK, curl) is served", func(t *testing.T) {
		resp := post(t, topology, "", "")
		require.NotEqual(t, http.StatusNotFound, resp.StatusCode)
	})
	t.Run("SystemService is exempt", func(t *testing.T) {
		resp := post(t, "/reliant.v1.SystemService/Version", testBrowserOrigin, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestClientVersionGatedProcedures(t *testing.T) {
	t.Parallel()
	for procedure, want := range map[string]bool{
		"/reliant.v1.ChatService/SendMessage":     true,
		"/reliant.v1.ToolsDaemonService/Connect":  true,
		"/reliant.v1.SystemService/Version":       false,
		"/reliant.v1.SystemService/Health":        false,
		"/forge.v1.Something/Do":                  false,
		"/grpc.health.v1.Health/Check":            false,
		"/health":                                 false,
		"/api/v2/terminal/ws":                     false,
		"/reliant.v1.SystemServiceExtended/Thing": true,
	} {
		require.Equal(t, want, clientVersionGated(procedure), procedure)
	}
}

// The gate is not wired outside reliant.v1 even when every switch is on:
// a direct check of a non-reliant.v1 procedure with no header passes.
func TestClientVersionGateIgnoresNonReliantProcedures(t *testing.T) {
	t.Parallel()
	gate := newClientVersionGate(&configv1.ClientVersionPolicy{
		Enforce: true, RefuseUnidentified: true, EnforceLegacyUnidentified: true,
		Cli: &configv1.CliClientVersionFloor{MinVersion: "9.0.0"},
	})
	for _, procedure := range []string{"/forge.v1.Something/Do", "/grpc.health.v1.Health/Check"} {
		require.NoError(t, gate.check(context.Background(), procedure, http.Header{}))
		require.NoError(t, gate.check(context.Background(), procedure, http.Header{"Reliant-Client": {"cli/1.0.0"}}))
	}

	edge := refuseLegacyUnidentified(&configv1.ClientVersionPolicy{RefuseUnidentified: true})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, path := range []string{"/health", "/api/oauth/callback", "/metrics", "/webhooks/github"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Origin", testBrowserOrigin)
		rec := httptest.NewRecorder()
		edge.ServeHTTP(rec, req)
		require.Equal(t, http.StatusTeapot, rec.Code, path)
	}
}

// Off by default: no policy, or one with every switch off, installs no edge.
func TestRefuseLegacyUnidentifiedIsOffByDefault(t *testing.T) {
	t.Parallel()
	for _, policy := range []*configv1.ClientVersionPolicy{nil, {Enforce: true}} {
		edge := refuseLegacyUnidentified(policy)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
		req := httptest.NewRequest(http.MethodPost, "/reliant.v1.ChatService/SendMessage", nil)
		req.Header.Set("Origin", testBrowserOrigin)
		rec := httptest.NewRecorder()
		edge.ServeHTTP(rec, req)
		require.Equal(t, http.StatusTeapot, rec.Code)
	}
}

// Every first-party client sends Reliant-Client; a browser that may not
// send it cross-origin would fail every RPC.
func TestCORSAllowsReliantClient(t *testing.T) {
	t.Parallel()
	handler := newCORSHandler([]string{webClientTestOrigin})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/reliant.v1.ChatService/SendMessage", nil)
	req.Header.Set("Origin", webClientTestOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "reliant-client")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.True(t, headerListContains(rec.Header().Get("Access-Control-Allow-Headers"), "reliant-client"),
		"preflight allows: %q", rec.Header().Get("Access-Control-Allow-Headers"))
}
