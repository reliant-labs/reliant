package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// The API server's environment is Reliant's, never the user's. A ${VAR} in a
// user's MCP config naming one of its variables must not be resolved here, or
// the server's secret lands in a config the user can read back. These tests
// set such a variable in THIS process and prove it reaches nothing.
const (
	serverOnlyVar    = "RELIANT_TEST_SERVER_ONLY_SECRET"
	serverOnlySecret = "server-secret-must-not-leak"
	serverOnlyRef    = "${" + serverOnlyVar + "}"
)

func TestConfigConverters_DoNotResolveReferencesFromTheServerEnvironment(t *testing.T) {
	t.Setenv(serverOnlyVar, serverOnlySecret)
	url := "https://mcp.example.com/" + serverOnlyRef

	fromProto := protoConfigToInternal(&reliantv1.MCPServerConfig{
		Command: serverOnlyRef,
		Args:    []string{"--token", serverOnlyRef, "$" + serverOnlyVar},
		Env:     []string{"TOKEN=" + serverOnlyRef},
		Headers: map[string]string{"Authorization": "Bearer " + serverOnlyRef},
		Url:     &url,
	})
	fromScoped := scopedConfigToInternal(MCPServerConfig{
		Command: serverOnlyRef,
		Args:    []string{"--token", serverOnlyRef, "$" + serverOnlyVar},
		Env:     map[string]string{"TOKEN": serverOnlyRef},
		Headers: map[string]string{"Authorization": "Bearer " + serverOnlyRef},
		URL:     url,
		Dir:     "/home/u/" + serverOnlyRef,
	})

	for name, cfg := range map[string]any{"protoConfigToInternal": fromProto, "scopedConfigToInternal": fromScoped} {
		encoded, err := json.Marshal(cfg)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), serverOnlySecret, "%s resolved a reference from the server's environment", name)
		require.Contains(t, string(encoded), serverOnlyRef, "%s must keep the reference as written", name)
	}
	require.Equal(t, serverOnlyRef, fromProto.Command)
	require.Equal(t, []string{"--token", serverOnlyRef, "$" + serverOnlyVar}, fromProto.Args)
	require.Equal(t, url, fromProto.URL)
	require.Equal(t, "Bearer "+serverOnlyRef, fromScoped.Headers["Authorization"])
}

// recordingDaemonRouter records every daemon command payload the service
// sends: what would cross NATS to the user's machine.
type recordingDaemonRouter struct {
	*fakeMCPDaemonRouter
	sent []string
}

func (r *recordingDaemonRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.sent = append(r.sent, commandType+" "+string(payload))
	return r.fakeMCPDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *recordingDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// End to end through the RPCs: install, restart, list and get. Neither the
// stored row, nor any RPC response, nor any payload sent to the daemon may
// carry the server's value; each carries the reference as the user wrote it.
func TestMCPService_ReferencesReachStoreResponsesAndDaemonUnresolved(t *testing.T) {
	t.Setenv(serverOnlyVar, serverOnlySecret)
	ctx, base, manager, projectID := setupMCPServiceToggleTest(t)
	manager.startErr = nil
	router := &recordingDaemonRouter{fakeMCPDaemonRouter: newFakeMCPDaemonRouter(manager)}
	svc := newMCPService(t, base.deps.Repo, router)

	require.NoError(t, svc.deps.Repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID,
		DaemonID:  "daemon-test",
		PushedAt:  time.Now().UTC(),
	}))

	url := "https://mcp.example.com/" + serverOnlyRef
	installs := map[string]*reliantv1.MCPServerConfig{
		"stdio-ref": {
			Command: serverOnlyRef,
			Args:    []string{"--token", serverOnlyRef},
			Env:     []string{"TOKEN=" + serverOnlyRef},
		},
		"http-ref": {
			Type:    "http",
			Url:     &url,
			Headers: map[string]string{"Authorization": "Bearer " + serverOnlyRef},
		},
	}
	var responses []any
	for name, cfg := range installs {
		resp, err := svc.InstallServer(ctx, connect.NewRequest(&reliantv1.InstallServerRequest{
			ProjectId: projectID,
			Name:      name,
			Scope:     reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
			Config:    cfg,
		}))
		require.NoError(t, err)
		require.True(t, resp.Msg.Success, "install %s: %s", name, resp.Msg.Message)
		responses = append(responses, resp.Msg)

		restart, err := svc.RestartServer(ctx, connect.NewRequest(&reliantv1.RestartServerRequest{ProjectId: projectID, Name: name}))
		require.NoError(t, err)
		require.True(t, restart.Msg.Success, "restart %s: %s", name, restart.Msg.Message)
		responses = append(responses, restart.Msg)

		got, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{ProjectId: projectID, Name: name}))
		require.NoError(t, err)
		responses = append(responses, got.Msg)
	}
	list, err := svc.ListServers(ctx, connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Servers, 2)
	responses = append(responses, list.Msg)

	record, err := svc.deps.Repo.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, record.MCPConfigs)

	for _, resp := range responses {
		encoded, err := json.Marshal(resp)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), serverOnlySecret, "an RPC response carries the server's value")
	}
	require.Contains(t, mustJSON(t, list.Msg), "Bearer "+serverOnlyRef, "ListServers must show the reference as written")

	require.NotContains(t, *record.MCPConfigs, serverOnlySecret, "the stored row carries the server's value")
	require.Contains(t, *record.MCPConfigs, serverOnlyRef, "the stored row must keep the reference as written")

	var manageServer, writeConfig int
	for _, sent := range router.sent {
		require.NotContains(t, sent, serverOnlySecret, "a daemon payload carries the server's value: %s", sent)
		switch {
		case strings.HasPrefix(sent, "mcp.manage_server ") && strings.Contains(sent, `"config":`):
			// add and restart carry the config the daemon launches; remove does not.
			manageServer++
			require.Contains(t, sent, serverOnlyRef)
		case strings.HasPrefix(sent, "mcp.write_config "):
			writeConfig++
			require.Contains(t, sent, serverOnlyRef)
		}
	}
	require.Positive(t, manageServer, "no mcp.manage_server payload was recorded")
	require.Positive(t, writeConfig, "no mcp.write_config payload was recorded")
}

// ${HOME} is the reference users actually write ("${HOME}/bin/server",
// "--root=${HOME}/code"), and every process has one, so it is the case a
// server-side expansion would silently get wrong: the server's home is not
// the user's. InstallServer must store it exactly as written.
func TestMCPService_InstallServerStoresHomeReferenceVerbatim(t *testing.T) {
	const serverHome = "/server/home/must-not-leak"
	t.Setenv("HOME", serverHome)
	ctx, svc, manager, projectID := setupMCPServiceToggleTest(t)
	manager.startErr = nil
	require.NoError(t, svc.deps.Repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID,
		DaemonID:  "daemon-test",
		PushedAt:  time.Now().UTC(),
	}))

	args := []string{"--root", "${HOME}/code", "--config=${HOME}/.config/server.json"}
	resp, err := svc.InstallServer(ctx, connect.NewRequest(&reliantv1.InstallServerRequest{
		ProjectId: projectID,
		Name:      "home-ref",
		Scope:     reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
		Config:    &reliantv1.MCPServerConfig{Command: "${HOME}/bin/server", Args: args},
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, resp.Msg.Message)

	record, err := svc.deps.Repo.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, record.MCPConfigs)
	require.NotContains(t, *record.MCPConfigs, serverHome, "the stored row carries the server's HOME")

	stored, ok, err := readScopeConfigFromStoredRecord(record, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT)
	require.NoError(t, err)
	require.True(t, ok, "no project-scope MCP config was stored")
	require.Contains(t, stored.MCPServers, "home-ref")
	require.Equal(t, "${HOME}/bin/server", stored.MCPServers["home-ref"].Command)
	require.Equal(t, args, stored.MCPServers["home-ref"].Args)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	return string(encoded)
}
