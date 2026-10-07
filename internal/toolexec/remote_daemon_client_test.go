package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TestServerSideFileTools_RunOnTheRunsMachine: view, write and edit execute on
// the worker and reach the user's files through a daemon client. That client
// must be bound to the machine the run's tools execute on, the selector
// ExecuteTools computed (the chat's pinned machine, else its worktree's
// owner), exactly like a daemon-placed tool such as shell. Bound to default
// resolution instead, a chat on machine B read and wrote files on machine A.
func TestServerSideFileTools_RunOnTheRunsMachine(t *testing.T) {
	router := &machineRouter{home: "/home/machine-b"}
	executor := NewRemoteExecutor(router)
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	executor.SetDaemonClientFactory(RemoteDaemonClients(router))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	result, err := executor.ExecuteTool(ctx, &ToolRequest{
		ToolName:       tools.ToolView,
		ToolInput:      `{"file_path":"README.md"}`,
		ToolCallID:     "call-1",
		UserID:         "user-1",
		ChatID:         "chat-1",
		ProjectID:      "project-1",
		ProjectPath:    "/home/machine-b/project",
		WorktreePath:   "/home/machine-b/project",
		DaemonSelector: &DaemonSelector{ID: "daemon-b"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	cmds := router.commands()
	require.NotEmpty(t, cmds, "view must reach a daemon")
	for _, cmd := range cmds {
		assert.Equal(t, "daemon-b", cmd.daemonID, "%s reached the wrong machine", cmd.commandType)
	}
}

// With no selector the run has no machine of its own and keeps default
// resolution, as before.
func TestServerSideFileTools_NoSelectorUsesDefault(t *testing.T) {
	router := &machineRouter{}
	executor := NewRemoteExecutor(router)
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	executor.SetDaemonClientFactory(RemoteDaemonClients(router))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	_, err := executor.ExecuteTool(ctx, &ToolRequest{
		ToolName: tools.ToolView, ToolInput: `{"file_path":"README.md"}`, ToolCallID: "call-1",
		UserID: "user-1", ChatID: "chat-1", ProjectID: "project-1",
		ProjectPath: "/p", WorktreePath: "/p",
	})
	require.NoError(t, err)

	cmds := router.commands()
	require.NotEmpty(t, cmds)
	for _, cmd := range cmds {
		assert.Equal(t, machineRouterDefault, cmd.daemonID)
	}
}
