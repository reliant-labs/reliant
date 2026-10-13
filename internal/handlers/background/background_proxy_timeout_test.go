// Copyright (c) 2025 Reliant Labs
package background

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// bgProxyRouter records the timeout the proxy asks the daemon for. It embeds
// toolexectest.Stub to satisfy the full DaemonRouter
// interface.
type bgProxyRouter struct {
	toolexectest.Stub
	lastCommandType string
	lastTimeoutMs   int32
	reply           []byte
	err             error
}

func (r *bgProxyRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *bgProxyRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, timeoutMs int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastTimeoutMs = timeoutMs
	return r.reply, r.err
}

// A process LIST must not be able to pin an API request for 30 seconds. Listing
// reads the daemon's in-memory table plus one batched port scan; nothing about
// it scales with the user's commands, so it gets a much tighter budget than
// bg_start / bg_kill do.
func TestBackgroundProxy_ListProcesses_UsesAShortDaemonTimeout(t *testing.T) {
	reply, err := json.Marshal([]daemonProcessInfo{})
	require.NoError(t, err)

	router := &bgProxyRouter{reply: reply}
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router}))

	_, err = svc.ListProcesses(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
	require.NoError(t, err)

	assert.Equal(t, "exec.bg_list", router.lastCommandType)
	assert.Equal(t, int32(5_000), router.lastTimeoutMs,
		"a list must fail fast rather than hold an API request for the 30s start/kill budget")
}

// Failing fast must not mean failing loud: the list budget running out on a
// busy machine is DeadlineExceeded (a WARN), not Internal (an ERROR and a
// Sentry event per poll). The proxies share daemonerr.MachineStateConnectError with the
// file tree, whose 2026-10-10 timeouts paged as "[gRPC] Request error".
func TestBackgroundProxy_DaemonTimeoutIsDeadlineExceeded(t *testing.T) {
	router := &bgProxyRouter{err: fmt.Errorf("daemon command exec.bg_list (subject s) via NATS failed: %w: %w",
		toolexec.ErrDaemonTimeout, context.DeadlineExceeded)}
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router}))

	_, err := svc.ListProcesses(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err), "error: %v", err)
}

// The mutating commands keep the longer budget: killing a process group can
// legitimately take a while, and cutting it would turn a slow kill into a
// spurious error.
func TestBackgroundProxy_KillProcess_KeepsTheLongerTimeout(t *testing.T) {
	reply, err := json.Marshal(struct{}{})
	require.NoError(t, err)

	router := &bgProxyRouter{reply: reply}
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router}))

	_, err = svc.KillProcess(AuthedContext(t), connect.NewRequest(&reliantv1.KillProcessRequest{
		ProcessId: "proc-1",
	}))
	require.NoError(t, err)

	assert.Equal(t, "exec.bg_kill", router.lastCommandType)
	assert.Equal(t, int32(30_000), router.lastTimeoutMs)
}
