// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is a clock a test moves by hand, shared by the manager and every
// process it starts.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var reapTestConfig = ReaperConfig{IdleAfter: 4 * time.Hour, VerdictTTL: 10 * time.Minute}

func newReapTestManager(t *testing.T) (*BackgroundManager, *fakeClock) {
	t.Helper()
	grace := KillGraceTimeout
	KillGraceTimeout = 200 * time.Millisecond
	t.Cleanup(func() { KillGraceTimeout = grace })
	clock := &fakeClock{now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	return &BackgroundManager{processes: map[string]*BackgroundProcess{}, clock: clock.Now}, clock
}

// startQuiet starts a command that prints nothing and runs until killed.
func startQuiet(t *testing.T, m *BackgroundManager, opts StartProcessOptions) *BackgroundProcess {
	t.Helper()
	if opts.Command == "" {
		opts.Command = "exec sleep 600"
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = t.TempDir()
	}
	process, err := m.StartProcess(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(-process.cmd.Process.Pid, syscall.SIGKILL) })
	return process
}

// The owner's rule (2026-11-02): a background process a chat left behind is
// stopped once it has been idle for the window AND the chat that started it
// has no run in progress. Each case below is one half of that rule failing,
// or one of its exemptions, against the case that is reaped.
func TestReapIdle_StopsOnlyAFinishedChatsIdleProcess(t *testing.T) {
	m, clock := newReapTestManager(t)

	finished := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done", ThreadID: "thread-1"})
	running := startQuiet(t, m, StartProcessOptions{ChatID: "chat-busy"})
	touched := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done"})
	keepAlive := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done", KeepAlive: true})
	noVerdict := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done"})
	notAChat := startQuiet(t, m, StartProcessOptions{PackageCommand: true})

	clock.Advance(3 * time.Hour)
	m.Touch(touched.ID) // an agent read its output an hour before the window ends
	clock.Advance(time.Hour + time.Minute)

	done := ChatVerdict{Running: false}
	m.RecordChatVerdicts(map[string]ChatVerdict{
		finished.ID:  done,
		running.ID:   {Running: true},
		touched.ID:   done,
		keepAlive.ID: done,
		notAChat.ID:  done,
	})

	got := m.ReapIdle(reapTestConfig)
	require.Equal(t, []string{finished.ID}, got, "only the finished chat's idle process is reaped")
	require.Equal(t, "killed", finished.StatusSafe())
	for name, p := range map[string]*BackgroundProcess{
		"its chat is running":                   running,
		"an agent touched it within the window": touched,
		"keep_alive":                            keepAlive,
		"the server never said its chat ended":  noVerdict,
		"no chat started it":                    notAChat,
	} {
		require.Equal(t, "running", p.StatusSafe(), "kept: %s", name)
	}

	// The output says why it stopped, for whoever reads it next.
	lines, _, err := m.GetCombinedOutputWithSeq(finished.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, lines)
	require.Contains(t, lines[len(lines)-1].Text, "idle for 4h1m")
	require.Contains(t, lines[len(lines)-1].Text, "chat chat-done has no run in progress")
	require.Contains(t, lines[len(lines)-1].Text, "keep_alive: true")
}

// Output is activity: a process still writing is not idle, whoever reads it.
func TestReapIdle_OutputKeepsAProcessActive(t *testing.T) {
	m, clock := newReapTestManager(t)
	dir := t.TempDir()
	trigger := filepath.Join(dir, "speak")
	process := startQuiet(t, m, StartProcessOptions{
		ChatID:     "chat-done",
		WorkingDir: dir,
		Command:    `while [ ! -e speak ]; do sleep 0.02; done; echo "GET / 200"; exec sleep 600`,
	})

	clock.Advance(4*time.Hour - time.Minute)
	require.NoError(t, os.WriteFile(trigger, nil, 0o600))
	require.Eventually(t, func() bool {
		out, _ := m.GetCombinedOutput(process.ID)
		return len(out) > 0
	}, 10*time.Second, 10*time.Millisecond)
	clock.Advance(2 * time.Minute)

	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Empty(t, m.ReapIdle(reapTestConfig), "it wrote output two minutes ago")
	clock.Advance(4 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Equal(t, []string{process.ID}, m.ReapIdle(reapTestConfig))
}

// Any output is activity, not only whole lines: a command printing progress
// dots with no newline for hours is not idle.
func TestReapIdle_PartialLineOutputKeepsAProcessActive(t *testing.T) {
	m, clock := newReapTestManager(t)
	dir := t.TempDir()
	process := startQuiet(t, m, StartProcessOptions{
		ChatID:     "chat-done",
		WorkingDir: dir,
		Command:    `while [ ! -e dot ]; do sleep 0.02; done; printf .; exec sleep 600`,
	})
	before := m.ReapOutlook(process).LastActive

	clock.Advance(4*time.Hour - time.Minute)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dot"), nil, 0o600))
	require.Eventually(t, func() bool { return m.ReapOutlook(process).LastActive.After(before) },
		10*time.Second, 10*time.Millisecond, "a write with no newline is activity")
	clock.Advance(2 * time.Minute)

	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Empty(t, m.ReapIdle(reapTestConfig), "it printed a progress dot two minutes ago")
}

// A foreground command detached into the background keeps writing into the
// buffers its foreground writers fill. Those writers stamp the activity the
// adopted process is judged by, so its output after the detach still counts.
func TestReapIdle_AdoptedCommandsOutputKeepsItActive(t *testing.T) {
	m, clock := newReapTestManager(t)
	dir := t.TempDir()
	activity := NewOutputActivity(clock.Now)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", `while [ ! -e speak ]; do sleep 0.02; done; echo still here; exec sleep 600`)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = activity.Writer(&stdout), activity.Writer(&stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	process, err := m.AdoptRunningProcess(AdoptRunningProcessOptions{
		Cmd: cmd, Command: "detached", WorkingDir: dir, ChatID: "chat-done", StartTime: clock.Now(),
		StdoutBuf: &stdout, StderrBuf: &stderr, WaitErrCh: waitCh, Activity: activity,
	})
	require.NoError(t, err)

	clock.Advance(4*time.Hour - time.Minute)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "speak"), nil, 0o600))
	require.Eventually(t, func() bool { return m.ReapOutlook(process).LastActive.Equal(clock.Now()) },
		10*time.Second, 10*time.Millisecond, "output after the detach is the adopted process's activity")
	clock.Advance(2 * time.Minute)

	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Empty(t, m.ReapIdle(reapTestConfig))
	clock.Advance(4 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Equal(t, []string{process.ID}, m.ReapIdle(reapTestConfig), "idle again after it fell silent")
}

// The reaper judges each candidate twice: once in the scan, and again right
// before its kill. An agent that uses the process in between wins.
func TestReapIdle_UseBetweenScanAndKillSparesTheProcess(t *testing.T) {
	m, clock := newReapTestManager(t)
	process := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done"})
	clock.Advance(5 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})

	beforeReap = func(p *BackgroundProcess) { m.Touch(p.ID) }
	t.Cleanup(func() { beforeReap = nil })
	require.Empty(t, m.ReapIdle(reapTestConfig))
	require.Equal(t, "running", process.StatusSafe())

	// The same holds for a verdict that the chat started running again.
	clock.Advance(5 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	beforeReap = func(p *BackgroundProcess) { m.RecordChatVerdicts(map[string]ChatVerdict{p.ID: {Running: true}}) }
	require.Empty(t, m.ReapIdle(reapTestConfig))
	require.Equal(t, "running", process.StatusSafe())
}

// The final look and the commitment to kill are one step, which output,
// touches and verdicts cannot interleave with: a touch that arrives while a
// reap is being claimed waits for the claim, and lands after the process is
// committed to being stopped, not between the look and the kill.
func TestReapIdle_ClaimHoldsOffUseUntilCommitted(t *testing.T) {
	m, clock := newReapTestManager(t)
	process := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done"})
	clock.Advance(5 * time.Hour)
	claimedAt := clock.Now()
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})

	touched, verdict := make(chan struct{}), make(chan struct{})
	duringReapClaim = func(p *BackgroundProcess) {
		go func() { m.Touch(p.ID); close(touched) }()
		go func() { m.RecordChatVerdicts(map[string]ChatVerdict{p.ID: {Running: true}}); close(verdict) }()
		select {
		case <-touched:
			t.Error("a touch completed inside the reap claim")
		case <-verdict:
			t.Error("a verdict was recorded inside the reap claim")
		case <-time.After(50 * time.Millisecond):
		}
		clock.Advance(time.Minute) // so a touch after the claim is visible as later
	}
	t.Cleanup(func() { duringReapClaim = nil })

	require.Equal(t, []string{process.ID}, m.ReapIdle(reapTestConfig), "committed: the late touch does not undo it")
	<-touched
	<-verdict
	require.Equal(t, "killed", process.StatusSafe())
	require.True(t, m.ReapOutlook(process).LastActive.After(claimedAt), "the touch landed, after the claim")

	// Claimed once: a second path (the workspace being removed) finds nothing to do.
	require.Zero(t, m.StopProcessesUnder(process.WorkingDir, "its workspace was removed"))
}

// An adopted command's own writers fill its buffers without the manager's
// lock. Reaping it while it writes must not race those writers (run under
// -race): the reap's note reaches the stderr buffer only once the command has
// stopped writing.
func TestReap_AdoptedCommandStillWritingDoesNotRace(t *testing.T) {
	m, clock := newReapTestManager(t)
	dir := t.TempDir()
	activity := NewOutputActivity(clock.Now)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", `while true; do echo out; echo err >&2; sleep 0.001; done`)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = activity.Writer(&stdout), activity.Writer(&stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	process, err := m.AdoptRunningProcess(AdoptRunningProcessOptions{
		Cmd: cmd, Command: "chatty", WorkingDir: dir, ChatID: "chat-1", StartTime: clock.Now(),
		StdoutBuf: &stdout, StderrBuf: &stderr, WaitErrCh: waitCh, Activity: activity,
	})
	require.NoError(t, err)

	require.Equal(t, 1, m.StopProcessesUnder(dir, "its workspace was removed"))
	require.Equal(t, "killed", process.StatusSafe())
	// The note is always in the combined output; the stderr buffer gets it
	// only if the command had already stopped writing when the reap ended.
	lines, _, err := m.GetCombinedOutputWithSeq(process.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, lines)
	require.Equal(t, "[reliant] Stopped this background process: its workspace was removed.", lines[len(lines)-1].Text)
	<-process.done
}

// A connector's touch reaches only its own processes; a first-party one, all.
func TestTouchForGrant(t *testing.T) {
	m, clock := newReapTestManager(t)
	mine := startQuiet(t, m, StartProcessOptions{ChatID: "c", GrantID: "grant-1"})
	theirs := startQuiet(t, m, StartProcessOptions{ChatID: "c"})
	started := clock.Now()

	clock.Advance(time.Hour)
	m.TouchForGrant("grant-1", mine.ID, theirs.ID, "never-started")
	require.Equal(t, clock.Now(), m.ReapOutlook(mine).LastActive)
	require.Equal(t, started, m.ReapOutlook(theirs).LastActive, "a connector cannot touch the user's process")

	clock.Advance(time.Hour)
	m.Touch(theirs.ID)
	require.Equal(t, clock.Now(), m.ReapOutlook(theirs).LastActive)
}

// The server's word goes stale: once it stops repeating that the chat has
// ended (it is unreachable, or stopped polling), the reaper does nothing.
// Unknown is not finished.
func TestReapIdle_StaleVerdictReapsNothing(t *testing.T) {
	m, clock := newReapTestManager(t)
	process := startQuiet(t, m, StartProcessOptions{ChatID: "chat-done"})
	clock.Advance(3 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	clock.Advance(2 * time.Hour)

	require.Empty(t, m.ReapIdle(reapTestConfig))
	require.Equal(t, "running", process.StatusSafe())
}

// An archived workspace's processes stop at once: idle or not, keep_alive or
// not, and without waiting for the next tick.
func TestReaper_ArchivedWorkspaceStopsItsProcessesAtOnce(t *testing.T) {
	m, _ := newReapTestManager(t)
	m.reapKick = make(chan struct{}, 1)
	busy := startQuiet(t, m, StartProcessOptions{ChatID: "chat-1"})
	server := startQuiet(t, m, StartProcessOptions{ChatID: "chat-1", KeepAlive: true})
	other := startQuiet(t, m, StartProcessOptions{ChatID: "chat-2"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunReaper(ctx, ReaperConfig{IdleAfter: 4 * time.Hour, Interval: time.Hour})

	m.Touch(busy.ID)
	m.RecordChatVerdicts(map[string]ChatVerdict{
		busy.ID:   {Running: true, WorkspaceArchived: true},
		server.ID: {WorkspaceArchived: true},
		other.ID:  {Running: true},
	})
	require.Eventually(t, func() bool {
		return busy.StatusSafe() == "killed" && server.StatusSafe() == "killed"
	}, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, "running", other.StatusSafe())
	// The reaper writes why into the output once the kill returns, just after
	// the status flips, so it is waited for, not read at once (reliant #2128).
	require.Eventually(t, func() bool {
		out, err := m.GetCombinedOutput(server.ID)
		if err != nil {
			return false
		}
		for _, line := range out {
			if strings.Contains(line.Text, "its workspace was archived") {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the archived workspace's server says in its output why it stopped")
}

// The daemon removing a workspace directory stops what runs inside it, and
// nothing outside it.
func TestStopProcessesUnder_StopsOnlyProcessesInsideTheDirectory(t *testing.T) {
	m, _ := newReapTestManager(t)
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "frontends", "web")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	sibling := workspace + "-sibling"
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	atRoot := startQuiet(t, m, StartProcessOptions{ChatID: "c", WorkingDir: workspace})
	inRepo := startQuiet(t, m, StartProcessOptions{PackageCommand: true, WorkingDir: nested})
	outside := startQuiet(t, m, StartProcessOptions{ChatID: "c", WorkingDir: sibling})

	require.Equal(t, 2, m.StopProcessesUnder(workspace, "its workspace was removed"))
	require.Equal(t, "killed", atRoot.StatusSafe())
	require.Equal(t, "killed", inRepo.StatusSafe())
	require.Equal(t, "running", outside.StatusSafe())
}

// Reaping is a kill of the whole process group: SIGTERM, then SIGKILL after
// the grace period for whatever ignored it. A dev server's shell dies on
// SIGTERM; the node child that traps it must not survive.
func TestReapIdle_SIGTERMThenSIGKILLTheWholeGroup(t *testing.T) {
	m, clock := newReapTestManager(t)
	dir := t.TempDir()
	ready, pidFile := filepath.Join(dir, "ready"), filepath.Join(dir, "pid")
	process := startQuiet(t, m, StartProcessOptions{
		ChatID:     "chat-done",
		WorkingDir: dir,
		Command: `( trap "" TERM; echo $(exec sh -c 'echo $PPID') > ` + pidFile + `; : > ` + ready + `; while true; do sleep 0.05; done ) >/dev/null 2>&1 &
wait`,
	})
	require.Eventually(t, func() bool { _, err := os.Stat(ready); return err == nil }, 30*time.Second, 10*time.Millisecond)
	b, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)

	clock.Advance(5 * time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{process.ID: {}})
	require.Equal(t, []string{process.ID}, m.ReapIdle(reapTestConfig))
	require.True(t, gone(grandchild), "the grandchild that ignored SIGTERM was SIGKILLed with its group")
	require.Equal(t, "killed", process.StatusSafe())
}

// shell_list shows when a process would be stopped, and why one never is.
func TestReapOutlook(t *testing.T) {
	m, clock := newReapTestManager(t)
	m.idleReap.Store(int64(4 * time.Hour))
	agent := startQuiet(t, m, StartProcessOptions{ChatID: "chat-1"})
	server := startQuiet(t, m, StartProcessOptions{ChatID: "chat-1", KeepAlive: true})
	panel := startQuiet(t, m, StartProcessOptions{PackageCommand: true})
	started := clock.Now()
	clock.Advance(time.Hour)
	m.RecordChatVerdicts(map[string]ChatVerdict{agent.ID: {Running: true}})

	got := m.ReapOutlook(agent)
	require.Equal(t, started, got.LastActive)
	require.NotNil(t, got.StopsAfter)
	require.Equal(t, started.Add(4*time.Hour), *got.StopsAfter)
	require.NotNil(t, got.ChatRunning)
	require.True(t, *got.ChatRunning)

	require.Nil(t, m.ReapOutlook(server).StopsAfter)
	require.Equal(t, "keep_alive", m.ReapOutlook(server).Exempt)
	require.Equal(t, "not started by a chat", m.ReapOutlook(panel).Exempt)
	require.Nil(t, m.ReapOutlook(server).ChatRunning, "no word from the server on it")

	m.idleReap.Store(0)
	require.Equal(t, "idle reaping is off", m.ReapOutlook(agent).Exempt)
}
