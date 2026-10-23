// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// activityOfChat reads the chats_with_activity value the sidebar and the run
// list both derive from.
func activityOfChat(t *testing.T, f *durableStatusFixture) int {
	t.Helper()
	chat, err := f.h.Repo().GetChat(context.Background(), f.chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.Activity)
	return *chat.Activity
}

const (
	activityRunning         = 1
	activityWaitingOnDaemon = 5
)

// A tool call that fails with ErrDaemonPending marks the run as waiting on its
// machine; the next call that reaches a daemon clears it. The error here is
// wrapped, exactly as the router returns it.
func TestExecuteTools_DaemonPendingSetsAndSuccessClears(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()
	f.h.CreateTestWorkflow(ctx, f.chatID, f.chatID)
	require.Equal(t, activityRunning, activityOfChat(t, f))

	pendingID := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetResult(pendingID, &toolexec.ToolResult{
		Success: false, IsError: true, Content: "Failed to execute tool on daemon",
		ErrorCode: toolexec.ErrorCodeDaemonUnreached, DaemonPending: true,
	})
	f.executeTool(t, pendingID, "bash", `{"command":"ls"}`, executor)
	assert.Equal(t, activityWaitingOnDaemon, activityOfChat(t, f), "pending tool result sets WAITING_FOR_DAEMON")

	// A server-side tool succeeding says nothing about the machine.
	serverID := "toolu_" + uuid.New().String()
	executor.SetResult(serverID, &toolexec.ToolResult{Success: true, Content: "ok"})
	f.executeTool(t, serverID, "web_search", `{"query":"x"}`, executor)
	assert.Equal(t, activityWaitingOnDaemon, activityOfChat(t, f), "a call that never touched a daemon does not clear it")

	okID := "toolu_" + uuid.New().String()
	executor.SetResult(okID, &toolexec.ToolResult{Success: true, Content: "ok", RanOnDaemon: true})
	f.executeTool(t, okID, "bash", `{"command":"ls"}`, executor)
	assert.Equal(t, activityRunning, activityOfChat(t, f), "the next call that reached the daemon clears it")
}

// The typed error can also arrive as the executor's error return rather than
// inside a result; errors.Is must see through the wrapping.
func TestExecuteTools_DaemonPendingFromWrappedExecutorError(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	f.h.CreateTestWorkflow(context.Background(), f.chatID, f.chatID)

	id := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetError(id, fmt.Errorf("resolve daemon: %w", toolexec.ErrDaemonPending))
	f.executeTool(t, id, "bash", `{"command":"ls"}`, executor)
	assert.Equal(t, activityWaitingOnDaemon, activityOfChat(t, f))
}

// While a tool call waits for its daemon to reconnect (a gateway rollout drops
// every daemon for ~20s), the run reads WAITING_FOR_DAEMON instead of hanging
// silently in RUNNING; the daemon coming back clears it, and a wait that ends
// without the daemon leaves it for the next call that reaches one.
func TestExecuteTools_ReconnectWaitShowsWaitingForDaemon(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()
	f.h.CreateTestWorkflow(ctx, f.chatID, f.chatID)
	a := NewExecuteToolsActivity(f.h.Repo(), newMockToolExecutor())
	observe := a.daemonWaitObserver(ctx, f.chatID)
	require.NotNil(t, observe)

	ended := observe("daemon-1")
	assert.Equal(t, activityWaitingOnDaemon, activityOfChat(t, f), "a waiting call shows the run waiting for its machine")
	ended(true)
	assert.Equal(t, activityRunning, activityOfChat(t, f), "the daemon coming back clears it")

	ended = observe("daemon-1")
	ended(false)
	assert.Equal(t, activityWaitingOnDaemon, activityOfChat(t, f), "a wait that gave up leaves the run waiting")

	assert.Nil(t, a.daemonWaitObserver(ctx, ""), "no chat, nothing to mark")
}

// A failure that is not "machine pending" must not touch the state.
func TestExecuteTools_OtherFailuresDoNotMarkDaemonPending(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	f.h.CreateTestWorkflow(context.Background(), f.chatID, f.chatID)

	id := "toolu_" + uuid.New().String()
	executor := newMockToolExecutor()
	executor.SetError(id, fmt.Errorf("boom"))
	f.executeTool(t, id, "bash", `{"command":"ls"}`, executor)
	assert.Equal(t, activityRunning, activityOfChat(t, f))
}
