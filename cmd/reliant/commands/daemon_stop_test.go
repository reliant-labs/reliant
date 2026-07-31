// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// lockedBuffer lets a test read output while the goroutine under test is still
// writing it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// writeRecordForPID stamps a runtime record naming pid, the way a live daemon
// would, so `stop` has something to read.
func writeRecordForPID(t *testing.T, dataDir string, pid int) {
	t.Helper()
	require.NoError(t, daemonstate.Init(dataDir, "http://localhost:29190", "h2c", false))
	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	state.PID = pid
	state.Stream = daemonstate.StreamConnected
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(daemonstate.Path(dataDir), data, 0o644))
}

func runDaemonStop(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"daemon", "stop", "--data-dir", dataDir}, args...))
	err := root.Execute()
	return out.String(), err
}

// startUnrelatedProcess runs body in a process that is NOT a descendant of the
// test — the shell that launches it exits immediately, so the process is
// reparented away and the test never reaps it.
//
// That shape is the point. `stop` never has the daemon as a child, and it waited
// on os.Process.Wait, which needs one: against a non-child Wait returns ECHILD
// in microseconds. Spawning the target as a direct child would also make it a
// zombie on exit — still "alive" to a signal-0 probe — which is a condition
// `stop` cannot encounter and must not be tested against.
func startUnrelatedProcess(t *testing.T, body string) int {
	t.Helper()
	// The child's stdout must be redirected away: it would otherwise inherit
	// this pipe and hold it open for its whole life, so Output() — which reads
	// to EOF — would never return.
	launcher := exec.Command("sh", "-c", body+` >/dev/null 2>&1 & echo $!`)
	out, err := launcher.Output()
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Signal(syscall.SIGKILL)
		}
	})

	require.Eventually(t, func() bool { return daemonProcessAlive(pid) }, 5*time.Second, 20*time.Millisecond)
	// Let the shell install its trap before anything signals it.
	time.Sleep(300 * time.Millisecond)
	return pid
}

// The defect. `stop` sent SIGTERM and then waited on os.Process.Wait, which
// against a non-child returns "wait: no child processes" immediately; the error
// was discarded, so the wait was a ~20us no-op, the 10-second escalation behind
// it was unreachable, and "Daemon stopped" was printed over a live daemon whose
// runtime record had just been deleted.
func TestStopDoesNotReportSuccessUntilTheProcessIsGone(t *testing.T) {
	dataDir := t.TempDir()
	pid := startUnrelatedProcess(t, `sh -c 'trap "" TERM; while :; do sleep 0.2; done'`)
	writeRecordForPID(t, dataDir, pid)

	out, err := runDaemonStop(t, dataDir)

	require.NoError(t, err, "SIGKILL escalation must succeed against a process that only ignores SIGTERM")
	require.False(t, daemonProcessAlive(pid),
		"stop returned, so the daemon must actually be gone")
	require.Contains(t, out, "escalating to SIGKILL",
		"the escalation must be reported, not silent")
	require.NotContains(t, out, "Daemon stopped (PID",
		"a process that ignored SIGTERM did not stop gracefully and must not be reported as if it had")

	_, readErr := daemonstate.Read(dataDir)
	require.True(t, os.IsNotExist(readErr), "a confirmed-dead daemon's record is removed")
}

// A daemon that exits on SIGTERM is reported as stopped, and only then.
func TestStopReportsSuccessWhenTheProcessExitsGracefully(t *testing.T) {
	dataDir := t.TempDir()
	pid := startUnrelatedProcess(t, `sh -c 'while :; do sleep 0.2; done'`)
	writeRecordForPID(t, dataDir, pid)

	out, err := runDaemonStop(t, dataDir)

	require.NoError(t, err)
	require.Contains(t, out, "Daemon stopped")
	require.False(t, daemonProcessAlive(pid))
	require.NotContains(t, out, "escalating")
}

// The case that started the incident, in the shape that matters: when the
// process cannot be killed, `stop` must say so, exit non-zero, and above all
// LEAVE THE RECORD. A missing record over a live process is strictly worse than
// a stale one — `status` then says "no daemon running" and `start` creates a
// second daemon under the same gateway identity.
func TestStopKeepsTheRecordAndFailsWhenTheProcessSurvives(t *testing.T) {
	dataDir := t.TempDir()
	writeRecordForPID(t, dataDir, 4242)

	var signals []syscall.Signal
	immortal := processControl{
		alive: func(int) bool { return true },
		signal: func(_ int, sig syscall.Signal) error {
			signals = append(signals, sig)
			return nil
		},
	}

	var out bytes.Buffer
	err := stopDaemonProcess(&out, immortal, dataDir, 4242, false)

	require.Error(t, err, "stop must not claim a success it has not verified")
	require.Contains(t, err.Error(), "still running")
	require.Contains(t, out.String(), "STILL RUNNING")
	require.Equal(t, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}, signals,
		"stop must escalate before giving up")

	state, readErr := daemonstate.Read(dataDir)
	require.NoError(t, readErr, "the record must survive: a live daemon has to stay visible")
	require.Equal(t, 4242, state.PID)
}

// A record naming a PID that is already gone is cleared without signalling
// anything — signalling a recycled PID would kill an unrelated process.
func TestStopClearsAStaleRecordWithoutSignalling(t *testing.T) {
	dataDir := t.TempDir()
	writeRecordForPID(t, dataDir, 4242)

	signalled := false
	dead := processControl{
		alive:  func(int) bool { return false },
		signal: func(int, syscall.Signal) error { signalled = true; return nil },
	}

	var out bytes.Buffer
	require.NoError(t, stopDaemonProcess(&out, dead, dataDir, 4242, false))
	require.False(t, signalled, "a dead PID must not be signalled")
	require.Contains(t, out.String(), "stale runtime record")

	_, readErr := daemonstate.Read(dataDir)
	require.True(t, os.IsNotExist(readErr))
}

func TestStopWithoutARecordSaysSo(t *testing.T) {
	out, err := runDaemonStop(t, t.TempDir())
	require.NoError(t, err)
	require.Contains(t, out, "No daemon running")
}

// --force does not get to skip verification either.
func TestForceStopStillVerifiesTheProcessIsGone(t *testing.T) {
	dataDir := t.TempDir()
	writeRecordForPID(t, dataDir, 4242)

	immortal := processControl{
		alive:  func(int) bool { return true },
		signal: func(int, syscall.Signal) error { return nil },
	}

	var out bytes.Buffer
	err := stopDaemonProcess(&out, immortal, dataDir, 4242, true)

	require.Error(t, err)
	require.NotContains(t, out.String(), "Daemon stopped")
	_, readErr := daemonstate.Read(dataDir)
	require.NoError(t, readErr, "the record must survive a failed force-kill too")
}

// An unattended daemon has nobody to press Ctrl+C a second time. Graceful
// shutdown walks code that can block on a peer that stopped reading or a child
// that ignores SIGTERM, so the whole path is bounded: when the deadline passes,
// the process exits rather than lingering with its gateway registration held.
func TestShutdownForceExitsWhenGracefulShutdownDoesNotFinish(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	canceled := make(chan struct{})
	exited := make(chan int, 1)
	var out lockedBuffer

	go watchShutdownSignals(sigCh, &out, func() { close(canceled) }, 150*time.Millisecond, func(code int) {
		exited <- code
	})

	sigCh <- syscall.SIGTERM
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the first signal must start a graceful shutdown")
	}

	select {
	case code := <-exited:
		require.Equal(t, 1, code)
	case <-time.After(5 * time.Second):
		t.Fatal("a graceful shutdown that never finishes must still end the process")
	}
	require.Contains(t, out.String(), "did not finish within")
}

// The interactive escape hatch still works, and beats the deadline.
func TestSecondSignalForceExitsImmediately(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	var out lockedBuffer

	go watchShutdownSignals(sigCh, &out, func() {}, time.Minute, func(code int) { exited <- code })

	sigCh <- syscall.SIGINT
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "Shutting down") },
		5*time.Second, 10*time.Millisecond)
	sigCh <- syscall.SIGINT

	select {
	case code := <-exited:
		require.Equal(t, 130, code)
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal must force-exit without waiting out the deadline")
	}
}
