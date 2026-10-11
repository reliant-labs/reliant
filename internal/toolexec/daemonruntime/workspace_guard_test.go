// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemon/rootwatch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// guardSpy records what a guard did to the daemon around it.
type guardSpy struct {
	mu        sync.Mutex
	stopped   bool
	exitCodes []int
}

func (s *guardSpy) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
}

func (s *guardSpy) exit(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitCodes = append(s.exitCodes, code)
}

func (s *guardSpy) snapshot() (bool, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped, append([]int(nil), s.exitCodes...)
}

// incidentWorkspace lays out a home directory with a worktree in it, the way
// the cloud workspace had /home/workspace/.reliant/worktrees/<project>/<name>.
// detach() then does to it what happened on 2026-10-09: the directory at the
// home path is no longer the one the daemon started with, and the worktree is
// not under it any more. (A rename stands in for the lazy unmount — both leave
// the path resolving to a different directory, which is what is detected.)
type incidentWorkspace struct {
	home     string
	worktree string
}

func newIncidentWorkspace(t *testing.T) incidentWorkspace {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no device/inode identity on windows")
	}
	base := t.TempDir()
	ws := incidentWorkspace{
		home:     filepath.Join(base, "workspace"),
		worktree: filepath.Join(base, "workspace", ".reliant", "worktrees", "reliant-labs", "errors-5972f1d1"),
	}
	require.NoError(t, os.MkdirAll(ws.worktree, 0o755))
	return ws
}

func (ws incidentWorkspace) detach(t *testing.T) {
	t.Helper()
	require.NoError(t, os.Rename(ws.home, ws.home+".detached"))
	require.NoError(t, os.Mkdir(ws.home, 0o755))
}

// newGuardedClient wires a daemon client the way Start does, with a real
// local executor, around a guard on home whose exit path is captured by spy.
func newGuardedClient(t *testing.T, home string, managed bool, spy *guardSpy) *daemonClient {
	t.Helper()
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		ShellPlatform: tools.ShellPlatformFromGOOS(runtime.GOOS),
	})
	localExec := toolexec.NewLocalToolExecutor(toolsFactory)
	localExec.SetDaemonClient(daemon.NewLocalClient())

	d := newTestDaemonClient("daemon-guard", "user-guard")
	d.localExecutor = localExec
	d.backgroundByReq = make(map[string]string)
	d.sendCh = make(chan *reliantv1.DaemonMessage, 8)
	d.sessionDone = make(chan struct{})

	guard, err := newWorkspaceGuard(home, managed, rootwatch.Options{})
	require.NoError(t, err)
	guard.cancelInFlight = d.cancelAllRequests
	guard.stopRuntime = spy.stop
	guard.exit = spy.exit
	guard.flushDelay = time.Millisecond
	guard.exitBackstop = time.Hour // the graceful path is what is under test
	guard.terminationLog = ""
	d.guard = guard
	return d
}

func shellRequest(t *testing.T, requestID, worktree string) *reliantv1.ToolRequest {
	t.Helper()
	input, err := json.Marshal(map[string]any{"command": "pwd"})
	require.NoError(t, err)
	ctxJSON, err := json.Marshal(map[string]any{
		"chat_id":  "chat-69386804",
		"worktree": map[string]any{"id": "wt-1", "path": worktree},
	})
	require.NoError(t, err)
	return &reliantv1.ToolRequest{
		RequestId:   requestID,
		ToolCallId:  requestID,
		ToolName:    tools.ShellToolName,
		ToolInput:   string(input),
		ContextJson: string(ctxJSON),
		TimeoutMs:   30_000,
	}
}

func toolResponse(t *testing.T, d *daemonClient) *reliantv1.ToolResponse {
	t.Helper()
	select {
	case msg := <-d.sendCh:
		resp := msg.GetToolResponse()
		require.NotNil(t, resp, "expected a tool response")
		return resp
	case <-time.After(10 * time.Second):
		t.Fatal("daemon sent no tool response")
		return nil
	}
}

// TestExecuteTool_LostWorkspaceVolume_SaysSo is the 2026-10-09 failure at the
// point the user saw it. Before the guard, the shell call failed with
// "working directory does not exist: <worktree>" — true, and useless: it sent
// agents looking for a deleted worktree, and nothing ever restarted the
// container. Now the call names the cause, and the managed daemon stops so
// kubelet restarts the container and re-attaches the volume.
func TestExecuteTool_LostWorkspaceVolume_SaysSo(t *testing.T) {
	ws := newIncidentWorkspace(t)
	spy := &guardSpy{}
	d := newGuardedClient(t, ws.home, true, spy)

	// Healthy: the call runs, and its worktree is recorded.
	d.executeTool(shellRequest(t, "req-before", ws.worktree))
	before := toolResponse(t, d)
	require.True(t, before.Success, "precondition: the shell call works before the detach: %s %s", before.Content, before.ErrorMessage)

	ws.detach(t)

	d.executeTool(shellRequest(t, "req-after", ws.worktree))
	resp := toolResponse(t, d)

	assert.False(t, resp.Success)
	assert.True(t, resp.IsError)
	assert.Equal(t, errorCodeWorkspaceVolumeLost, resp.ErrorCode, "content: %s", resp.Content)
	assert.Contains(t, resp.Content, "The workspace volume was lost")
	assert.Contains(t, resp.Content, "restarting")
	assert.NotContains(t, resp.Content, "working directory does not exist")

	require.Eventually(t, func() bool {
		stopped, _ := spy.snapshot()
		return stopped
	}, 5*time.Second, 5*time.Millisecond, "the managed daemon must stop so the container restarts")
	var lost *WorkspaceVolumeLostError
	require.ErrorAs(t, d.guard.lostVolume(), &lost)
	assert.True(t, lost.Fault.Home)
}

// TestDaemonCommand_LostWorkspaceVolume_SaysSo covers the other door into the
// daemon: UI-issued commands (exec.run, terminals, file tree) carry no
// project context, so only a fault covering everything — a lost $HOME —
// refuses them.
func TestDaemonCommand_LostWorkspaceVolume_SaysSo(t *testing.T) {
	ws := newIncidentWorkspace(t)
	spy := &guardSpy{}
	d := newGuardedClient(t, ws.home, true, spy)

	ws.detach(t)

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "cmd-1",
		CommandType: "exec.run",
		Payload:     mustPayload(t, daemon.RunCommandRequest{Command: "pwd", WorkingDir: ws.worktree}),
	})

	assert.False(t, resp.Success, "payload: %s", resp.Payload)
	assert.Contains(t, resp.ErrorMessage, "The workspace volume was lost")
	assert.NotContains(t, string(resp.Payload), "working directory does not exist")
}

// TestWorkspaceGuard_ManagedHomeFault_CancelsInFlightAndStops: in-flight work
// is cancelled so its replies (rewritten to the cause) reach the gateway, then
// the runtime is stopped, and a backstop exits if shutdown wedges.
func TestWorkspaceGuard_ManagedHomeFault_CancelsInFlightAndStops(t *testing.T) {
	ws := newIncidentWorkspace(t)
	spy := &guardSpy{}
	d := newGuardedClient(t, ws.home, true, spy)
	d.guard.exitBackstop = time.Millisecond

	inFlight, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.registerCancel("req-in-flight", cancel)

	ws.detach(t)
	d.guard.watcher.CheckAll()

	select {
	case <-inFlight.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight work was not cancelled")
	}
	require.Eventually(t, func() bool {
		stopped, codes := spy.snapshot()
		return stopped && len(codes) == 1 && codes[0] != 0
	}, 5*time.Second, 5*time.Millisecond, "runtime stopped, then the backstop exits non-zero")

	assert.NotNil(t, d.guard.verdict(ws.worktree), "a call that was in flight reports the cause")
}

// TestWorkspaceGuard_DesktopHomeFault_KeepsRunning: with no supervisor that
// could re-attach anything, the daemon stays up and refuses work, saying what
// happened and what to do.
func TestWorkspaceGuard_DesktopHomeFault_KeepsRunning(t *testing.T) {
	ws := newIncidentWorkspace(t)
	spy := &guardSpy{}
	d := newGuardedClient(t, ws.home, false, spy)

	inFlight, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.registerCancel("req-in-flight", cancel)

	ws.detach(t)
	refused := d.guard.preflight(ws.worktree)

	require.NotNil(t, refused)
	assert.Equal(t, errorCodeWorkspaceRootChanged, refused.ErrorCode)
	assert.Contains(t, refused.Content, "restart the Reliant daemon")
	assert.NotContains(t, refused.Content, "machine is restarting")

	time.Sleep(20 * time.Millisecond)
	stopped, codes := spy.snapshot()
	assert.False(t, stopped, "a desktop daemon never stops itself")
	assert.Empty(t, codes)
	assert.NoError(t, inFlight.Err(), "a desktop daemon does not cancel in-flight work")
	assert.NoError(t, d.guard.lostVolume())
}

// TestWorkspaceGuard_RemovedWorktree_IsNotAFault: deleting a worktree is the
// normal life of a worktree, not a lost volume.
func TestWorkspaceGuard_RemovedWorktree_IsNotAFault(t *testing.T) {
	ws := newIncidentWorkspace(t)
	spy := &guardSpy{}
	d := newGuardedClient(t, ws.home, true, spy)
	d.guard.observe(ws.worktree)

	require.NoError(t, os.RemoveAll(ws.worktree))
	d.guard.watcher.CheckAll()

	assert.Nil(t, d.guard.preflight(ws.worktree))
	assert.NoError(t, d.guard.commandPreflight())
	stopped, _ := spy.snapshot()
	assert.False(t, stopped)
}

// TestStart_ManagedDaemonExitsWhenItsHomeVolumeIsLost runs the real runtime:
// a managed daemon whose $HOME is replaced under it must return from Start
// with WorkspaceVolumeLostError — which `reliant daemon start` turns into a
// non-zero exit, which kubelet answers with a container restart — instead of
// staying up against the wrong disk. The runtime dials a gateway that refuses
// connections, so this is the reconnect loop the 2026-10-09 daemon sat in.
func TestStart_ManagedDaemonExitsWhenItsHomeVolumeIsLost(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: waits out the guard's reply-flush delay (2s); runs in the full lane")
	}
	ws := newIncidentWorkspace(t)
	t.Setenv("HOME", ws.home)
	t.Setenv(DaemonTypeEnvVar, "managed")
	previousInterval, previousExit := workspaceGuardPollInterval, workspaceGuardExit
	workspaceGuardPollInterval = 10 * time.Millisecond
	// The backstop stays armed after Start returns; it must not take the
	// test binary down with it.
	workspaceGuardExit = func(int) {}
	t.Cleanup(func() { workspaceGuardPollInterval, workspaceGuardExit = previousInterval, previousExit })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Start(ctx, StartOptions{BootstrapConfig: testBootstrapConfig(t.TempDir())})
	}()

	// Let the runtime come up and record $HOME before pulling it away.
	time.Sleep(200 * time.Millisecond)
	ws.detach(t)

	select {
	case err := <-done:
		var lost *WorkspaceVolumeLostError
		require.ErrorAs(t, err, &lost, "Start must report the lost volume, got: %v", err)
		assert.True(t, lost.Fault.Home)
		assert.Equal(t, filepath.Clean(ws.home), lost.Fault.Path)
		assert.NoError(t, ctx.Err(), "Start stopped itself; the caller did not cancel it")
	case <-time.After(20 * time.Second):
		t.Fatal("a managed daemon whose home volume was lost kept running")
	}
}

func TestRestartHeals_OnlyManagedHome(t *testing.T) {
	home := rootwatch.Fault{Path: "/home/workspace", Home: true}
	root := rootwatch.Fault{Path: "/home/workspace/projects/x"}
	assert.True(t, restartHeals(home, true))
	assert.False(t, restartHeals(root, true), "a restart re-attaches the pod's volume and nothing else")
	assert.False(t, restartHeals(home, false))
	assert.False(t, restartHeals(root, false))
}

func TestWriteTerminationMessage_OnlyWhereKubeletProvidesTheFile(t *testing.T) {
	dir := t.TempDir()

	absent := filepath.Join(dir, "absent")
	writeTerminationMessage(absent, "volume lost")
	_, err := os.Stat(absent)
	assert.True(t, errors.Is(err, os.ErrNotExist), "never creates the file")

	present := filepath.Join(dir, "termination-log")
	require.NoError(t, os.WriteFile(present, []byte("stale message from an earlier run"), 0o644))
	long := make([]byte, terminationMessageLimit+100)
	for i := range long {
		long[i] = 'x'
	}
	writeTerminationMessage(present, string(long))
	got, err := os.ReadFile(present)
	require.NoError(t, err)
	assert.Len(t, got, terminationMessageLimit, "truncated to kubelet's limit, old content replaced")
}

func TestNilWorkspaceGuard_IsInert(t *testing.T) {
	var g *workspaceGuard
	g.observe("/x")
	g.run(context.Background())
	assert.Nil(t, g.preflight("/x"))
	assert.Nil(t, g.verdict("/x"))
	assert.NoError(t, g.commandPreflight())
	assert.NoError(t, g.commandVerdict())
	assert.NoError(t, g.lostVolume())
}
