// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
	streaminghandler "github.com/reliant-labs/reliant/internal/handlers/streaming"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// testRouter stands in for the daemon router. Any call panics on the nil
// embedded DaemonRouter, which is how a test says "no daemon is reached here".
type testRouter struct{ toolexec.DaemonRouter }

// testGrants stands in for the connector grant store, on the same terms.
type testGrants struct{ connectorgrant.Store }

// testRoute mounts what one test serves: a service (mountService), or the
// API's plain-HTTP routes (testHTTPRoutes). The API root's own route set is
// internal/app's; these tests pin the listener, the chain and the handlers
// behind it, so each mounts only what it calls.
type testRoute func(mux *http.ServeMux, opts ...connect.HandlerOption) []string

// mountService mounts svc with the listener's chain and the mount's own
// options over it, as the generated route set does.
func mountService(name string, svc mountkit.Registrar, mount ...mountkit.Option) testRoute {
	return func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		mountkit.RegisterService(mux, svc, opts, mount...)
		return []string{name}
	}
}

// plainRoutes mounts plain-HTTP routes of a test's own, outside the chain.
func plainRoutes(mounts ...func(mux *http.ServeMux)) testRoute {
	return func(mux *http.ServeMux, _ ...connect.HandlerOption) []string {
		for _, mount := range mounts {
			mount(mux)
		}
		return nil
	}
}

// testUserAuth is the user authenticator under API-key auth; nothing these
// tests send reaches its token authority.
func testUserAuth(t *testing.T) *UserAuth {
	t.Helper()
	a, err := NewUserAuth(auth.Settings{Mode: auth.ModeAPIKey, APIKey: drainTestAPIKey}, drainTestAuthority{})
	require.NoError(t, err)
	return a
}

// testStreaming is the real StreamingService over repo, with no hubs.
func testStreaming(t *testing.T, repo db.Repository) testRoute {
	t.Helper()
	svc, err := streaminghandler.New(streaminghandler.Deps{Logger: slog.Default(), Config: &config.Config{}, Repo: repo})
	require.NoError(t, err)
	return mountService("reliant.v1.StreamingService", svc)
}

// testForge is the real ForgeService, with mount's options over the chain.
func testForge(t *testing.T, mount ...mountkit.Option) testRoute {
	t.Helper()
	svc, err := forge.New(forge.Deps{Logger: slog.Default(), Config: &config.Config{}, Router: testRouter{}, Repo: drainTestRepo{}})
	require.NoError(t, err)
	return mountService("reliant.v1.ForgeService", svc, mount...)
}

// testCatalog is the real CatalogService answering ListInputOptions from
// options (none: the rpc is unimplemented).
func testCatalog(t *testing.T, options catalog.InputOptions) testRoute {
	t.Helper()
	svc, err := catalog.New(catalog.Deps{Logger: slog.Default(), Config: &config.Config{}, InputOptions: options})
	require.NoError(t, err)
	return mountService("reliant.v1.CatalogService", svc)
}

// testHTTPRoutes are the API's plain-HTTP routes over stand-ins these tests
// never call through.
func testHTTPRoutes(t *testing.T, authn *UserAuth) testRoute {
	t.Helper()
	routes, err := NewAPIHTTPRoutes(APIHTTPRoutesDeps{
		Auth:    authn,
		Repo:    drainTestRepo{},
		Router:  testRouter{},
		OAuth:   connections.NewOAuthHTTP(nil),
		Inbound: webhook.NewInbound(nil, nil, nil, nil, ""),
		Grants:  testGrants{},
		Tokens:  drainTestAuthority{},
	})
	require.NoError(t, err)
	return func(mux *http.ServeMux, _ ...connect.HandlerOption) []string {
		routes.Register(mux)
		return nil
	}
}

// startTestServer serves routes under API-key auth as the api root does —
// through a UserChain, on an APIListener — on a loopback port, returning the
// listener and its base URL.
func startTestServer(t *testing.T, routes ...testRoute) (*APIListener, string) {
	t.Helper()
	chain, err := NewUserChain(UserChainDeps{Auth: testUserAuth(t)})
	require.NoError(t, err)
	listener, err := NewAPIListener(APIListenerDeps{
		BindAddress: "127.0.0.1",
		Chain:       chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			var services []string
			for _, route := range routes {
				services = append(services, route(mux, opts...)...)
			}
			return services
		}),
	})
	require.NoError(t, err)
	require.NoError(t, listener.Listen(context.Background()))

	runCtx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- listener.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-served)
	})
	return listener, "http://" + listener.Addr().String()
}

// The plain-HTTP routes beside the Connect services authenticate themselves:
// the terminal websocket and the connector MCP endpoint answer an anonymous
// caller 401, not 404.
func TestAPIHTTPRoutesAreMounted(t *testing.T) {
	_, baseURL := startTestServer(t, testHTTPRoutes(t, testUserAuth(t)))

	for _, path := range []string{"/api/v2/terminal/ws", mcpserver.MountPath} {
		t.Run(path, func(t *testing.T) {
			resp, err := h2cClient().Get(baseURL + path)
			require.NoError(t, err)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

// /health on the API port is what the web client reads the auth mode from
// (web/src/hooks/useAuthMode.ts): its JSON is unchanged.
func TestAPIHealthReportsTheAuthMode(t *testing.T) {
	_, baseURL := startTestServer(t, testHTTPRoutes(t, testUserAuth(t)))

	resp, err := h2cClient().Get(baseURL + "/health")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, map[string]string{"status": "ok", "service": "grpc-server", "auth_mode": auth.ModeAPIKey}, body)
}

func TestAPIHTTPRoutesRequireTheirCollaborators(t *testing.T) {
	_, err := NewAPIHTTPRoutes(APIHTTPRoutesDeps{})
	require.Error(t, err)
}
