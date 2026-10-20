// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// testRouter stands in for the daemon router. Any call panics on the nil
// embedded DaemonRouter, which is how a test says "no daemon is reached here".
type testRouter struct{ toolexec.DaemonRouter }

// testGrants stands in for the connector grant store, on the same terms.
type testGrants struct{ connectorgrant.Store }

// testServerConfig returns a Config with every required collaborator set to
// a stand-in that construction accepts and these tests never call through:
// the requests they send end in the auth interceptor or in the one handler
// under test, which repo answers.
func testServerConfig(t *testing.T, repo db.Repository) *Config {
	t.Helper()
	// Neither client dials until it is used, and nothing here uses them.
	temporal, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1"})
	require.NoError(t, err)
	t.Cleanup(temporal.Close)
	sqlDB, err := sql.Open("pgx", "postgres://127.0.0.1:1/unreached")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return &Config{
		Database:        repo,
		SQL:             sqlDB,
		ConnectorGrants: testGrants{},
		DaemonRouter:    testRouter{},
		TokenAuthority:  drainTestAuthority{},
		TemporalClient:  temporal,
		PauseService:    workflow.NewPauseService(temporal, repo),
		ToolsFactory:    tools.NewToolsFactory(nil),
		CatalogSearch:   catalogindex.NewService(nil, nil, nil),
		Connections:     connections.NewService(nil, nil, nil, nil, nil, nil),
		OAuthRoutes:     connections.NewOAuthHTTP(nil),
		TriggerInbound:  webhook.NewInbound(nil, nil, nil, nil, ""),
	}
}

// startTestServer builds the API from cfg with NewAPI, under API-key auth, and
// serves it as serverapi.Run does — through a UserChain, on an APIListener —
// on a loopback port, returning the listener and its base URL. extra mounts
// routes of the test's own beside the API's.
func startTestServer(t *testing.T, cfg *Config, extra ...func(mux *http.ServeMux)) (*APIListener, string) {
	t.Helper()
	cfg.Auth = auth.Settings{Mode: auth.ModeAPIKey, APIKey: drainTestAPIKey}

	api, err := NewAPI(cfg)
	require.NoError(t, err)
	chain, err := NewUserChain(UserChainDeps{Auth: api.Auth()})
	require.NoError(t, err)
	listener, err := NewAPIListener(APIListenerDeps{
		BindAddress: "127.0.0.1",
		Chain:       chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			services := api.Register(mux, opts...)
			for _, mount := range extra {
				mount(mux)
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

// A missing collaborator is a composition bug: serverapi.Run builds every one
// or fails boot. NewAPI refuses it at construction, naming it, rather than
// leaving its routes unmounted — an unmounted route answers 404, which the web
// client files as version skew.
func TestNewAPINamesEveryMissingCollaborator(t *testing.T) {
	required := []string{
		"Database", "SQL", "ConnectorGrants", "DaemonRouter", "TemporalClient", "PauseService",
		"ToolsFactory", "CatalogSearch", "Connections", "OAuthRoutes", "TriggerInbound",
	}

	t.Run("an empty config names them all", func(t *testing.T) {
		_, err := NewAPI(&Config{})
		require.Error(t, err)
		for _, name := range required {
			require.Contains(t, err.Error(), name)
		}
	})

	unset := map[string]func(*Config){
		"Database":        func(c *Config) { c.Database = nil },
		"SQL":             func(c *Config) { c.SQL = nil },
		"ConnectorGrants": func(c *Config) { c.ConnectorGrants = nil },
		"DaemonRouter":    func(c *Config) { c.DaemonRouter = nil },
		"TemporalClient":  func(c *Config) { c.TemporalClient = nil },
		"PauseService":    func(c *Config) { c.PauseService = nil },
		"ToolsFactory":    func(c *Config) { c.ToolsFactory = nil },
		"CatalogSearch":   func(c *Config) { c.CatalogSearch = nil },
		"Connections":     func(c *Config) { c.Connections = nil },
		"OAuthRoutes":     func(c *Config) { c.OAuthRoutes = nil },
		"TriggerInbound":  func(c *Config) { c.TriggerInbound = nil },
	}
	require.Len(t, unset, len(required), "every required collaborator has a case")
	for _, name := range required {
		t.Run("missing "+name, func(t *testing.T) {
			cfg := testServerConfig(t, drainTestRepo{})
			unset[name](cfg)
			_, err := NewAPI(cfg)
			require.Error(t, err)
			require.Equal(t, "api config: required collaborators not set: "+name, err.Error())
		})
	}
}

// Every service is mounted, whatever the deployment: an unauthenticated call
// reaches the auth interceptor and is refused there, rather than finding no
// route (a 404, which Connect reports as Unimplemented). These are the
// services that used to be mounted only when a collaborator was present.
func TestNewAPIMountsEveryService(t *testing.T) {
	_, baseURL := startTestServer(t, testServerConfig(t, drainTestRepo{}))

	for _, procedure := range []string{
		reliantv1connect.FileSystemServiceGetFileTreeProcedure,
		reliantv1connect.PackageCommandsServiceListCommandsProcedure,
		reliantv1connect.BackgroundServiceListProcessesProcedure,
		reliantv1connect.TerminalServiceListSessionsProcedure,
		reliantv1connect.AccountServicePreviewAccountDeletionProcedure,
		reliantv1connect.ConnectorServiceListConnectorsProcedure,
		reliantv1connect.ConnectionServiceListIntegrationsProcedure,
		reliantv1connect.ForgeServiceGetTopologyProcedure,
	} {
		t.Run(strings.TrimPrefix(procedure, "/reliant.v1."), func(t *testing.T) {
			// An empty message is a valid encoding of every request type.
			call := connect.NewClient[emptypb.Empty, emptypb.Empty](h2cClient(), baseURL+procedure)
			_, err := call.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
			require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "got %v", err)
			require.Contains(t, err.Error(), "missing authorization token")
		})
	}

	// The plain-HTTP routes beside them: the terminal websocket and the
	// connector MCP endpoint answer 401, not 404.
	for _, path := range []string{"/api/v2/terminal/ws", mcpserver.MountPath} {
		t.Run(path, func(t *testing.T) {
			resp, err := h2cClient().Get(baseURL + path)
			require.NoError(t, err)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

// The name each mount reports is what the listener checks against its
// chain's audience, so it must be the service that mount really serves: a
// name copied onto the wrong row would check one service's audience and
// serve another's.
func TestAPIServiceNamesAreTheServicesMounted(t *testing.T) {
	cfg := testServerConfig(t, drainTestRepo{})
	cfg.Auth = auth.Settings{Mode: auth.ModeAPIKey, APIKey: drainTestAPIKey}
	api, err := NewAPI(cfg)
	require.NoError(t, err)
	require.NotEmpty(t, api.services)

	seen := map[string]bool{}
	for _, s := range api.services {
		require.False(t, seen[s.name], "%s is mounted twice", s.name)
		seen[s.name] = true

		mux := http.NewServeMux()
		mountkit.RegisterService(mux, s.svc, nil, s.mount...)
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, "/"+s.name+"/Any", nil))
		require.Equal(t, "/"+s.name+"/", pattern, "the %s row mounts another service", s.name)
	}
}
