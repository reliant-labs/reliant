// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// startGrandchildCommand starts, through the background manager, a shell that
// leaves a background grandchild ignoring SIGTERM (the shape of `nohup task ci
// &` or a server whose shell is the thing that got killed), and returns the
// process and the grandchild's pid once the grandchild is armed. With
// exitShell the shell exits right away, leaving the grandchild behind.
func startGrandchildCommand(t *testing.T, exitShell bool) (*BackgroundManager, *BackgroundProcess, int) {
	t.Helper()
	// The grandchild ignores SIGTERM, so every kill here waits out the grace
	// period before its SIGKILL; a short one keeps the tests fast.
	grace := KillGraceTimeout
	KillGraceTimeout = 200 * time.Millisecond
	t.Cleanup(func() { KillGraceTimeout = grace })
	dir := t.TempDir()
	ready, pidFile := filepath.Join(dir, "ready"), filepath.Join(dir, "pid")
	tail := "wait"
	if exitShell {
		tail = "exit 0"
	}
	command := `( trap "" TERM; echo $BASHPID > ` + pidFile + `; : > ` + ready + `; while true; do sleep 0.05; done ) >/dev/null 2>&1 &
` + tail

	m := &BackgroundManager{processes: map[string]*BackgroundProcess{}}
	process, err := m.StartProcess(context.Background(), StartProcessOptions{Command: command, WorkingDir: dir})
	require.NoError(t, err)
	pgid := process.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		require.True(t, time.Now().Before(deadline), "the grandchild never armed its trap")
		time.Sleep(10 * time.Millisecond)
	}
	b, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)
	return m, process, grandchild
}

// gone reports whether pid has exited (a zombie awaiting its reaper counts).
func gone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return err == nil && strings.Contains(string(stat), ") Z ")
}

// shell_kill must take the whole process group down, not just the shell it
// started. The shell dies on SIGTERM at once; its grandchild ignores SIGTERM.
// The kill used to end its wait when the shell exited and leave the grandchild
// running, orphaned (2026-10-30: killed shells left `task ci` and heavy-lock.sh
// children behind, part of what ran the machine out of processes).
func TestKillProcessKillsGrandchildThatIgnoresSIGTERM(t *testing.T) {
	m, process, grandchild := startGrandchildCommand(t, false)

	require.NoError(t, m.KillProcess(process.ID))

	require.True(t, gone(grandchild), "grandchild %d survived shell_kill", grandchild)
	if members, ok := osutil.GroupMembers(process.cmd.Process.Pid); ok {
		require.Empty(t, members, "the process group still has live members")
	}
	require.Equal(t, "killed", process.StatusSafe())
}

// A background command whose shell already exited is not finished while its
// process group lives: shell_kill must still reach what it left running,
// rather than refuse with "not running" and leave it orphaned.
func TestKillProcessReachesWhatAnExitedShellLeftRunning(t *testing.T) {
	m, process, grandchild := startGrandchildCommand(t, true)

	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the shell never exited")
	}
	require.False(t, gone(grandchild), "precondition: the grandchild outlives its shell")

	require.NoError(t, m.KillProcess(process.ID))
	require.True(t, gone(grandchild), "grandchild %d survived shell_kill after its shell exited", grandchild)

	// Nothing left: the next kill says so.
	require.Error(t, m.KillProcess(process.ID))
}

// A command that exited long ago keeps its record, and its process group id
// can since have been reused by an unrelated group — another command, another
// agent's workload. shell_kill on the old record must not signal it: the id
// is the command's only while a process it left is provably still in it.
func TestKillProcessNeverSignalsARecycledGroup(t *testing.T) {
	if _, ok := osutil.GroupMembers(1); !ok {
		t.Skip("needs /proc to tell a group's members apart")
	}
	// The "recycled" group: an unrelated process leading a group whose id is
	// the old command's pid.
	victim := exec.Command("sleep", "300")
	victim.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, victim.Start())
	t.Cleanup(func() { _ = victim.Process.Kill(); _ = victim.Wait() })

	m := &BackgroundManager{processes: map[string]*BackgroundProcess{}}
	old := &BackgroundProcess{
		ID: "old", Status: "completed", cmd: &exec.Cmd{Process: victim.Process}, done: make(chan struct{}),
		// What the old command left behind when it exited: a process with this
		// pid but another start time, long gone.
		survivors: []osutil.GroupMember{{PID: victim.Process.Pid, Start: 1}},
	}
	close(old.done)
	m.processes["old"] = old

	require.Error(t, m.KillProcess("old"), "a group no survivor proves is the command's must be refused")
	time.Sleep(200 * time.Millisecond)
	require.False(t, gone(victim.Process.Pid), "shell_kill on a stale record killed an unrelated process group")

	// With no survivors recorded at all, likewise.
	old.survivors = nil
	require.Error(t, m.KillProcess("old"))
	require.False(t, gone(victim.Process.Pid))
}

// A zombie runs nothing. A group whose only member left is an unreaped zombie
// — its reaper never comes, as under a PID 1 that does not reap — is gone,
// not a survivor that SIGKILL failed to stop. Here this test process is the
// zombie's parent and does not reap it until the end.
func TestGroupWatchIgnoresUnreapedZombies(t *testing.T) {
	if _, ok := osutil.GroupMembers(1); !ok {
		t.Skip("needs /proc to tell a group's members apart")
	}
	z := exec.Command("sleep", "0.3")
	z.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, z.Start())
	w := watchGroup(z.Process.Pid)
	require.True(t, w.alive(), "a running member is alive")

	deadline := time.Now().Add(30 * time.Second)
	for {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(z.Process.Pid) + "/stat")
		require.NoError(t, err, "the zombie was reaped by someone else")
		if strings.Contains(string(stat), ") Z ") {
			break
		}
		require.True(t, time.Now().Before(deadline), "the child never became a zombie")
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, processGroupAlive(z.Process.Pid), "precondition: the group id still answers a signal")
	require.False(t, w.alive(), "a zombie-only group counted as alive")
	require.True(t, waitProcessGroupGone(w, nil, time.Second), "the wait spent its budget on a zombie")
	_ = z.Wait()
}
