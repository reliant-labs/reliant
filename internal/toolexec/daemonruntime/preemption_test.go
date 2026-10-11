// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// newWaitingClient wires a daemon client with a real local executor and a
// background process for shell_wait to wait on.
func newWaitingClient(t *testing.T) (*daemonClient, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: spawns and kills a real background process; runs in the full lane")
	}
	local := daemon.NewLocalClient()
	localExec := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{
		ShellPlatform: tools.ShellPlatformFromGOOS(runtime.GOOS),
	}))
	localExec.SetDaemonClient(local)

	d := newTestDaemonClient("daemon-preempt", "user-preempt")
	d.localExecutor = localExec
	d.backgroundByReq = make(map[string]string)
	d.sendCh = make(chan *reliantv1.DaemonMessage, 8)
	d.sessionDone = make(chan struct{})

	processID, err := local.StartBackground(context.Background(), &daemon.RunCommandRequest{
		Command:    "exec sleep 30",
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = local.KillProcess(context.Background(), processID) })
	return d, processID
}

// startWait dispatches a shell_wait on processID and returns once the daemon
// has registered it, so a cancel sent afterwards reaches it.
func startWait(t *testing.T, d *daemonClient, callID, processID string) {
	t.Helper()
	input, err := json.Marshal(map[string]any{"process_id": processID, "timeout_seconds": 60})
	require.NoError(t, err)
	ctxJSON, err := json.Marshal(map[string]any{
		"chat_id":  "chat-preempt",
		"worktree": map[string]any{"path": t.TempDir()},
	})
	require.NoError(t, err)

	go d.executeTool(&reliantv1.ToolRequest{
		RequestId:   callID,
		ToolCallId:  callID,
		ToolName:    tools.ToolShellWait,
		ToolInput:   string(input),
		ContextJson: string(ctxJSON),
		TimeoutMs:   120_000,
	})
	require.Eventually(t, func() bool {
		d.cancelMu.Lock()
		defer d.cancelMu.Unlock()
		return d.cancelByReq[callID] != nil
	}, 5*time.Second, 5*time.Millisecond, "the wait never started")
}

func cancelWith(t *testing.T, d *daemonClient, callID, reason string) {
	t.Helper()
	require.NoError(t, d.handleServerMessage(context.Background(), &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolCancel{
			ToolCancel: &reliantv1.ToolExecutionCancel{RequestId: callID, Reason: reason},
		},
	}))
}

// A preempted wait sends back its OWN answer — "stopped waiting, still
// running" — instead of the generic "cancelled" every other stop reports. That
// answer is the whole point of ending the wait early.
func TestExecuteTool_PreemptedWaitSendsItsOwnAnswer(t *testing.T) {
	d, processID := newWaitingClient(t)
	startWait(t, d, "toolu_preempted", processID)

	cancelWith(t, d, "toolu_preempted", toolexec.PreemptedCancelReason)
	resp := toolResponse(t, d)

	assert.True(t, resp.Success, "content: %s", resp.Content)
	assert.False(t, resp.IsError, "content: %s", resp.Content)
	assert.Contains(t, resp.Content, "Stopped waiting")
	assert.Contains(t, resp.Content, "has not been killed")
}

// Any other stop still reports as cancelled: the daemon keeps a tool's own
// answer only when it was told the stop is a preemption.
func TestExecuteTool_CancelledWaitStillReportsCancelled(t *testing.T) {
	d, processID := newWaitingClient(t)
	startWait(t, d, "toolu_cancelled", processID)

	cancelWith(t, d, "toolu_cancelled", "user interrupted the agent")
	resp := toolResponse(t, d)

	assert.False(t, resp.Success)
	assert.Equal(t, "CANCELLED", resp.ErrorCode)
}
