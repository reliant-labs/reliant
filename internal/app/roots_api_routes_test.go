// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/account"
	"github.com/reliant-labs/reliant/internal/handlers/approval"
	"github.com/reliant-labs/reliant/internal/handlers/attachment"
	"github.com/reliant-labs/reliant/internal/handlers/background"
	"github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/connection"
	"github.com/reliant-labs/reliant/internal/handlers/connector"
	"github.com/reliant-labs/reliant/internal/handlers/daemon"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/handlers/file_system"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
	"github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/handlers/mcp"
	"github.com/reliant-labs/reliant/internal/handlers/message"
	"github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
	"github.com/reliant-labs/reliant/internal/handlers/package_commands"
	"github.com/reliant-labs/reliant/internal/handlers/plan"
	"github.com/reliant-labs/reliant/internal/handlers/preset"
	"github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/handlers/provider_account"
	"github.com/reliant-labs/reliant/internal/handlers/question"
	"github.com/reliant-labs/reliant/internal/handlers/repo"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/handlers/settings"
	streaminghandler "github.com/reliant-labs/reliant/internal/handlers/streaming"
	"github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/handlers/token"
	"github.com/reliant-labs/reliant/internal/handlers/tool_call"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/handlers/usage"
	workflowhandler "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/handlers/worktree"
)

const routesTestAPIKey = "api-routes-test-key"

// userServices are the 36 services the api-server has always mounted on its
// listener (grpc.NewAPI's list, before the api became a root).
var userServices = []string{
	reliantv1connect.SystemServiceName, reliantv1connect.PlanServiceName, reliantv1connect.TaskServiceName,
	reliantv1connect.CatalogServiceName, reliantv1connect.ProjectServiceName, reliantv1connect.WorktreeServiceName,
	reliantv1connect.RepoServiceName, reliantv1connect.ApprovalServiceName, reliantv1connect.QuestionServiceName,
	reliantv1connect.ChatServiceName, reliantv1connect.RunServiceName, reliantv1connect.MessageServiceName,
	reliantv1connect.SettingsServiceName, reliantv1connect.ProviderAccountServiceName, reliantv1connect.MCPServiceName,
	reliantv1connect.WorkflowServiceName, reliantv1connect.ScenarioServiceName, reliantv1connect.FileSystemServiceName,
	reliantv1connect.PackageCommandsServiceName, reliantv1connect.ToolCallServiceName, reliantv1connect.ForgeServiceName,
	reliantv1connect.StreamingServiceName, reliantv1connect.AttachmentServiceName, reliantv1connect.PresetServiceName,
	reliantv1connect.TriggerServiceName, reliantv1connect.InboxServiceName, reliantv1connect.DaemonRegistryServiceName,
	reliantv1connect.ModelEndpointServiceName, reliantv1connect.TokenServiceName, reliantv1connect.DaemonServiceName,
	reliantv1connect.AccountServiceName, reliantv1connect.ConnectionServiceName, reliantv1connect.ConnectorServiceName,
	reliantv1connect.BackgroundServiceName, reliantv1connect.TerminalServiceName, reliantv1connect.UsageServiceName,
}

// testUserRoutes is the api's route set over services that are never called
// through: every request these tests send ends in the chain (auth, or a
// mount's own interceptor) before a handler runs.
func testUserRoutes(t *testing.T, opts MountOptions) *UserRoutes {
	t.Helper()
	routes, err := NewUserRoutes(UserRoutesDeps{
		Account: &account.Service{}, Approval: &approval.Service{}, Attachment: &attachment.Service{},
		Background: &background.Service{}, Connection: &connection.Service{}, Worktree: &worktree.Service{},
		Chat: &chat.Service{}, Run: &run.Service{}, Trigger: &trigger.Service{}, Catalog: &catalog.Service{},
		Connector: &connector.Service{}, Daemon: &daemon.Service{}, DaemonRegistry: &daemon_registry.Service{},
		FileSystem: &file_system.Service{}, Forge: &forge.Service{}, Inbox: &inbox.Service{}, MCP: &mcp.Service{},
		Message: &message.Service{}, ModelEndpoint: &model_endpoint.Service{}, PackageCommands: &package_commands.Service{},
		Task: &task.Service{}, Plan: &plan.Service{}, Preset: &preset.Service{}, Project: &project.Service{},
		ProviderAccount: &provider_account.Service{}, Question: &question.Service{}, Repo: &repo.Service{},
		Scenario: &scenario.Service{}, Settings: &settings.Service{}, Streaming: &streaminghandler.Service{},
		System: &system.Service{}, Terminal: &terminal.Service{}, Token: &token.Service{}, ToolCall: &tool_call.Service{},
		Workflow: &workflowhandler.Service{}, Usage: &usage.Service{},
		Logger:  slog.Default(),
		Options: opts,
	})
	require.NoError(t, err)
	return routes
}

// nobodysTokens stands in for the token authority: API-key callers never
// reach it.
type nobodysTokens struct{ accesstokenclient.Introspector }

// serveUserRoutes serves routes through the user chain under API-key auth,
// as the api root's listener does, returning its base URL.
func serveUserRoutes(t *testing.T, routes *UserRoutes) string {
	t.Helper()
	authn, err := grpcserver.NewUserAuth(auth.Settings{Mode: auth.ModeAPIKey, APIKey: routesTestAPIKey}, nobodysTokens{})
	require.NoError(t, err)
	chain, err := grpcserver.NewUserChain(grpcserver.UserChainDeps{Auth: authn})
	require.NoError(t, err)
	listener, err := grpcserver.NewAPIListener(grpcserver.APIListenerDeps{BindAddress: "127.0.0.1", Chain: chain, Routes: routes})
	require.NoError(t, err)
	require.NoError(t, listener.Listen(context.Background()))
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- listener.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-served)
	})
	return "http://" + listener.Addr().String()
}

func h2c() *http.Client {
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

// The route set mounts exactly the services the api-server has always
// served, each under its own name — the name the listener checks against its
// chain's audience, so a name reported for another service's mount would
// check one service's audience and serve another's.
func TestAPIMountsEveryUserService(t *testing.T) {
	mux := http.NewServeMux()
	names := testUserRoutes(t, apiMountOptions(false)).Register(mux)

	want := append([]string(nil), userServices...)
	sort.Strings(want)
	got := append([]string(nil), names...)
	sort.Strings(got)
	require.Equal(t, want, got)

	for _, name := range names {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, "/"+name+"/Any", nil))
		require.Equal(t, "/"+name+"/", pattern, "%s is not mounted under its own name", name)
	}
}

// Every service is mounted, whatever the deployment: an unauthenticated call
// reaches the auth interceptor and is refused there, rather than finding no
// route (a 404, which the web client files as version skew).
func TestAPIServicesAnswerBehindAuth(t *testing.T) {
	baseURL := serveUserRoutes(t, testUserRoutes(t, apiMountOptions(false)))
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
			call := connect.NewClient[emptypb.Empty, emptypb.Empty](h2c(), baseURL+procedure)
			_, err := call.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
			require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "got %v", err)
		})
	}
}

// The mount options the api root gives its services: machinewake on the
// worktree and filesystem services, and, under RELIANT_DISABLE_FORGE_API,
// the forge API's refusal on ForgeService — after auth, before its handler.
func TestAPIMountOptions(t *testing.T) {
	on := apiMountOptions(false)
	require.Len(t, on.Worktree, 1, "worktree requests wake an asleep machine")
	require.Len(t, on.FileSystem, 1, "filesystem requests wake an asleep machine")
	require.Empty(t, on.Forge, "the forge API is on unless the operator turns it off")

	off := apiMountOptions(true)
	require.Len(t, off.Forge, 1)
	baseURL := serveUserRoutes(t, testUserRoutes(t, off))
	client := reliantv1connect.NewForgeServiceClient(h2c(), baseURL)

	req := connect.NewRequest(&reliantv1.GetForgeTopologyRequest{})
	req.Header().Set("Authorization", "Bearer "+routesTestAPIKey)
	_, err := client.GetTopology(context.Background(), req)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "want a Connect error, got %v", err)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code(), "got %v", err)
	require.Equal(t, "forge_api_disabled", connectErr.Meta().Get(svcerr.ReasonHeader))

	_, err = client.GetTopology(context.Background(), connect.NewRequest(&reliantv1.GetForgeTopologyRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "an anonymous caller is refused for its credential first")
}
