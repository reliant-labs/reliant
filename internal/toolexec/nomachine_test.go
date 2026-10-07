// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The transport is the last line: a no-machine run's tool menu and its
// execution refusal sit above it, so a marked context arriving here means one
// of those was bypassed. The router must still not RESOLVE a daemon — resolving
// is what can resume a suspended one through the control plane.
func TestNoMachineContextNeverResolvesOrResumesADaemon(t *testing.T) {
	router, reg := newSuspendedRouter(t)
	ctx := nomachine.With(context.Background())

	_, err := router.SendToolRequestSync(ctx, "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"})
	require.ErrorIs(t, err, nomachine.ErrNoMachine)

	_, err = router.SendToolRequestSyncWithSelector(ctx, "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"}, pinned)
	require.ErrorIs(t, err, nomachine.ErrNoMachine)

	_, err = router.SendDaemonCommand(ctx, "u", "mcp.ensure_loaded", []byte(`{}`), 1000)
	require.ErrorIs(t, err, nomachine.ErrNoMachine)

	_, err = router.EnsureAwake(ctx, "u", pinned)
	require.ErrorIs(t, err, nomachine.ErrNoMachine)

	assert.Zero(t, reg.resumes.Load(), "a run with no machine must never resume one")
}

// The refusal is not an offline signal. The breaker pauses a run after three
// "no daemon connected" results; a run with no machine by design must never be
// paused waiting for a machine nobody is going to start.
func TestNoMachineRefusalIsNotADaemonOfflineSignal(t *testing.T) {
	assert.False(t, daemonoffline.IsError(nomachine.ErrNoMachine))
	assert.False(t, daemonoffline.IsToolResultContent(nomachine.Refusal("shell")))
}

// A daemon-placed tool in a no-machine run is refused before dispatch, and no
// server-side tool is handed a daemon client to reach the user's disk with.
func TestRemoteExecutorKeepsNoMachineRunsOffTheDaemon(t *testing.T) {
	router := &recordingDaemonRouter{}
	executor := NewRemoteExecutor(router)
	server := &LocalToolExecutor{}
	executor.SetServerExecutor(server)
	var handedClient daemon.Client
	executor.SetDaemonClientFactory(func(string, *DaemonSelector) daemon.Client {
		handedClient = daemon.NewLocalClient()
		return handedClient
	})
	ctx := nomachine.With(context.Background())

	res, err := executor.ExecuteTool(ctx, placementTestRequest("shell"))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Equal(t, nomachine.Refusal("shell"), res.Content)
	assert.Empty(t, router.toolsSent, "a daemon-placed tool must never reach the router")

	_, err = executor.ExecuteTool(ctx, placementTestRequest("fetch"))
	require.NoError(t, err)
	assert.Nil(t, handedClient, "a server-side tool in a no-machine run must not get a daemon client")
}

// The daemon MCP binder binds nothing for a no-machine run, so no MCP runtime
// exists to send mcp.* commands through.
func TestDaemonMCPBinderBindsNothingForANoMachineRun(t *testing.T) {
	binder := NewDaemonMCPContextBinder(&recordingDaemonRouter{})
	ctx := nomachine.With(contextWithUser("u"))
	bound := binder.Bind(rctx.NewToolContext(ctx, "c", "t", nil, nil))
	assert.Nil(t, bound.MCP)

	// Control: an ordinary run still binds one.
	bound = binder.Bind(rctx.NewToolContext(contextWithUser("u"), "c", "t", nil, nil))
	assert.NotNil(t, bound.MCP)
}

func contextWithUser(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}
