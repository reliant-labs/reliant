// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStarterEnv makes this test binary a stand-in daemon (runTestStarter): it
// starts one sandboxed command, prints the sandbox init's pid, and waits to be
// killed.
const testStarterEnv = "CREDSANDBOX_TEST_STARTER"

// runTestStarter is the stand-in daemon, when asked to be one, and never
// returns; it returns at once otherwise.
func runTestStarter() {
	if os.Getenv(testStarterEnv) != "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	// It ignores SIGTERM, so only SIGKILL ends its sandbox: a SIGTERM to an
	// orphaned init would be forwarded to it and lost.
	cmd := exec.Command("bash", "-c", `trap '' TERM; exec sleep 600`)
	cmd.Dir = os.Getenv("CREDSANDBOX_TEST_DIR")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if refusal := Apply(cmd, self, Plan{}); refusal != "" {
		fmt.Println("error:", refusal)
		os.Exit(2)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	Release(cmd)
	fmt.Println(cmd.Process.Pid)
	select {}
}

// procAlive reports whether pid is the process that started at ticks and has
// not exited (a zombie has exited).
func procAlive(pid int, ticks string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	f := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return len(f) > 19 && f[19] == ticks && f[0] != "Z" && f[0] != "X"
}

// childOf is a process whose parent is pid, or 0.
func childOf(pid int) int {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		f := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
		if len(f) > 1 && f[1] == strconv.Itoa(pid) {
			return n
		}
	}
	return 0
}

// procComm is pid's name from its stat, which stays readable for an
// undumpable process.
func procComm(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(raw)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return ""
	}
	return s[open+1 : end]
}

func procStartTicks(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	require.NoError(t, err)
	f := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return f[19]
}

// The init dies with the process that started it, and its whole namespace with
// it, even when its command ignores SIGTERM: an orphaned init is
// uninspectable and held every worktree reclaim on a machine for hours.
func TestSandbox_InitDiesWithTheProcessThatStartedIt(t *testing.T) {
	requireSandbox(t)
	starter := exec.Command(selfPath(t))
	starter.Env = []string{testStarterEnv + "=1", "CREDSANDBOX_TEST_DIR=" + filepath.Dir(t.TempDir()),
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")} //nolint:forbidigo // the test's own PATH for the command under test
	stdout, err := starter.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, starter.Start())
	t.Cleanup(func() { _ = starter.Process.Kill(); _ = starter.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "the starter said nothing")
	initPID, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err, "the starter: %s", line)
	initTicks := procStartTicks(t, initPID)

	// The command is running under the init before its starter goes. The
	// init is undumpable, so its children file is unreadable: find the
	// command by its parent in stat, which stays readable.
	var cmdPID int
	var cmdTicks string
	// Waited for until it is sleep: past bash's `trap '' TERM`, so it ignores
	// SIGTERM, and a SIGTERM to the init could not end the sandbox.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && cmdPID == 0; time.Sleep(20 * time.Millisecond) {
		if c := childOf(initPID); c != 0 && procComm(c) == "sleep" {
			cmdPID = c
		}
	}
	require.NotZero(t, cmdPID, "the sandboxed command never started")
	cmdTicks = procStartTicks(t, cmdPID)
	require.True(t, procAlive(initPID, initTicks))

	require.NoError(t, starter.Process.Signal(syscall.SIGKILL))
	_ = starter.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (procAlive(initPID, initTicks) || procAlive(cmdPID, cmdTicks)) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.False(t, procAlive(initPID, initTicks), "the sandbox init outlived the process that started it")
	assert.False(t, procAlive(cmdPID, cmdTicks), "the sandboxed command outlived its init")
}

// While its starter lives, the init runs its command to the end as before, and
// the command inherits neither the liveness pipe nor its variable.
func TestSandbox_InitRunsItsCommandWhileItsStarterLives(t *testing.T) {
	requireSandbox(t)
	out, code := run(t, Plan{}, `sleep 1; echo done; ls /proc/self/fd | tr '\n' ' '; echo; env | grep -c RELIANT_CRED_SANDBOX || true`)
	require.Equal(t, 0, code, out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, out)
	assert.Equal(t, "done", lines[0])
	assert.Equal(t, "0 1 2 3", strings.TrimSpace(lines[1]), "the command inherited a descriptor of the sandbox's (3 is ls's own)")
	assert.Equal(t, "0", lines[2], "the command inherited the helper's variables")
}

// A starter that exited before the init could ask to die with it sends no
// signal: the init finds its liveness pipe hung up and never runs the command.
func TestSandbox_InitRefusesWhenItsStarterIsAlreadyGone(t *testing.T) {
	requireSandbox(t)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, w.Close())
	t.Cleanup(func() { _ = r.Close() })
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command("bash", "-c", "touch "+marker)
	cmd.Dir = filepath.Dir(t.TempDir())
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")} //nolint:forbidigo // the test's own PATH for the command under test
	require.Empty(t, apply(cmd, selfPath(t), Plan{}, func() (*os.File, error) { return r, nil }))
	out, err := cmd.CombinedOutput()
	Release(cmd)
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "%s", out)
	assert.Equal(t, RefusedExitCode, ee.ExitCode(), "%s", out)
	assert.Contains(t, string(out), "the process that started this sandbox has exited")
	assert.NoFileExists(t, marker, "the command ran with nobody left to end it")
}
