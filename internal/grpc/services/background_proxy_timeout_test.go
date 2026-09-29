// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// bgProxyRouter records the timeout the proxy asks the daemon for. It embeds
// worktreeTestDaemonRouter (worktree_test.go) to satisfy the full DaemonRouter
// interface.
type bgProxyRouter struct {
	worktreeTestDaemonRouter
	lastCommandType string
	lastTimeoutMs   int32
	reply           []byte
}

func (r *bgProxyRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *bgProxyRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, timeoutMs int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastTimeoutMs = timeoutMs
	return r.reply, nil
}

// A process LIST must not be able to pin an API request for 30 seconds. Listing
// reads the daemon's in-memory table plus one batched port scan; nothing about
// it scales with the user's commands, so it gets a much tighter budget than
// bg_start / bg_kill do.
func TestBackgroundProxy_ListProcesses_UsesAShortDaemonTimeout(t *testing.T) {
	reply, err := json.Marshal([]daemonProcessInfo{})
	require.NoError(t, err)

	router := &bgProxyRouter{reply: reply}
	svc := NewBackgroundProxyService(router)

	_, err = svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
	require.NoError(t, err)

	assert.Equal(t, "exec.bg_list", router.lastCommandType)
	assert.Equal(t, int32(5_000), router.lastTimeoutMs,
		"a list must fail fast rather than hold an API request for the 30s start/kill budget")
}

// The mutating commands keep the longer budget: killing a process group can
// legitimately take a while, and cutting it would turn a slow kill into a
// spurious error.
func TestBackgroundProxy_KillProcess_KeepsTheLongerTimeout(t *testing.T) {
	reply, err := json.Marshal(struct{}{})
	require.NoError(t, err)

	router := &bgProxyRouter{reply: reply}
	svc := NewBackgroundProxyService(router)

	_, err = svc.KillProcess(authedCtx(), connect.NewRequest(&reliantv1.KillProcessRequest{
		ProcessId: "proc-1",
	}))
	require.NoError(t, err)

	assert.Equal(t, "exec.bg_kill", router.lastCommandType)
	assert.Equal(t, int32(30_000), router.lastTimeoutMs)
}
