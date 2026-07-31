// Copyright (c) 2025 Reliant Labs
package daemonstate

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lockHolderEnv makes the test binary re-execute itself as a process that takes
// the claim and then blocks. Re-exec beats compiling a helper: it exercises the
// real Acquire, and it needs no toolchain at test time.
const lockHolderEnv = "DAEMONSTATE_LOCK_HOLDER_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(lockHolderEnv); dir != "" {
		lock, err := Acquire(dir)
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
		os.Stdout.WriteString("held\n")
		_ = os.Stdout.Sync()
		// Block until killed; Release is never called. KeepAlive matters: the
		// claim lives on the open file, and os.File's finalizer would close it
		// (dropping the lock) the moment the value became unreachable.
		time.Sleep(5 * time.Minute)
		runtime.KeepAlive(lock)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The second claim on a data directory must be refused. Without this, `reliant
// daemon start` run against an already-running daemon produced a second daemon
// that resolved to the same gateway daemonID; the gateway evicted the incumbent
// on every registration and the two evicted each other for hours.
func TestAcquireRefusesASecondClaimOnTheSameDataDir(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	second, err := Acquire(dir)
	require.Nil(t, second)
	require.ErrorIs(t, err, ErrLocked)
	require.Contains(t, err.Error(), LockFileName, "the refusal must name the claim it lost")
}

func TestAcquireSucceedsAfterRelease(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, first.Release())
	require.NoError(t, first.Release(), "releasing twice is not an error")

	second, err := Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, second.Release())
}

// Separate data dirs are separate claims — a per-project daemon must still be
// able to run alongside another one.
func TestAcquireIsPerDataDir(t *testing.T) {
	a, err := Acquire(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Release() })

	b, err := Acquire(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, b.Release())
}

// The claim must survive the runtime record being deleted. This is the exact
// condition that started the incident: `daemon stop` removed daemon-state.json
// while the daemon was still alive, so nothing on disk said a daemon was
// running and the next `start` created a second one.
func TestClaimSurvivesTheRuntimeRecordBeingDeleted(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))

	held, err := Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Release() })

	require.NoError(t, Clear(dir))
	_, readErr := Read(dir)
	require.True(t, os.IsNotExist(readErr), "the record is gone")

	_, err = Acquire(dir)
	require.ErrorIs(t, err, ErrLocked, "a deleted record must not make a live daemon invisible")
}

// The kernel drops the claim when its holder dies, however it died. That is why
// the claim is an OS lock and not a PID in a file: a crashed daemon must not
// block the next start, and a reused PID must not be mistaken for one.
func TestClaimIsReleasedWhenTheHolderProcessDies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL-equivalent teardown differs on Windows")
	}
	dir := t.TempDir()

	holder := exec.Command(os.Args[0], "-test.run=NoSuchTest")
	holder.Env = append(os.Environ(), lockHolderEnv+"="+dir)
	stdout, err := holder.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, holder.Start())
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "holder must announce it took the claim")
	require.Equal(t, "held\n", line)

	_, err = Acquire(dir)
	require.ErrorIs(t, err, ErrLocked, "another live process holds it")

	require.NoError(t, holder.Process.Kill())
	_, _ = holder.Process.Wait()

	// The holder never called Release; the kernel drops the lock on death.
	require.Eventually(t, func() bool {
		l, err := Acquire(dir)
		if err != nil {
			return false
		}
		_ = l.Release()
		return true
	}, 10*time.Second, 50*time.Millisecond, "a dead holder must not keep the claim")
}

// An embedded runtime publishes nothing and claims nothing.
func TestAcquireWithoutADataDirIsInert(t *testing.T) {
	l, err := Acquire("")
	require.NoError(t, err)
	require.Empty(t, l.Path())
	require.NoError(t, l.Release())

	again, err := Acquire("")
	require.NoError(t, err)
	require.NoError(t, again.Release())
}
