package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// The generate lock (.forge/forge.lock) exists for one scenario: two forge
// runs — usually two agents sharing a checkout — regenerating the same
// project at once. These tests drive it across REAL processes: this test
// binary re-exec'd as TestGenerateLockHelperProcess.
//
// The helpers do not run a full `forge generate`. One costs 20-50s and needs a
// scaffolded project with node_modules, which is out of budget for the unit
// tier, and the property under test is the lock around the pipeline rather
// than the pipeline. Each helper therefore runs a stand-in critical section: a
// deliberately non-atomic read-modify-write of a shared counter, which loses
// updates exactly the way two pipelines interleaving writes to one generated
// file do. TestGenerateCmd_WaitsForConcurrentRun covers the wiring from the
// real `generate` command to the lock.

const (
	generateLockHelperEnv    = "FORGE_GENERATE_LOCK_HELPER"
	generateLockHelperDirEnv = "FORGE_GENERATE_LOCK_DIR"
)

// TestGenerateLockHelperProcess is not a test. Re-exec'd with
// FORGE_GENERATE_LOCK_HELPER set, it acts as one forge run in the project at
// FORGE_GENERATE_LOCK_DIR:
//
//	increment  take the lock, read-sleep-write the counter file, release.
//	hold       take the lock, write the "held" marker (holding our pid), then
//	           keep the lock until the "release" marker appears.
func TestGenerateLockHelperProcess(t *testing.T) {
	mode := os.Getenv(generateLockHelperEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(generateLockHelperDirEnv)
	release, err := acquireGenerateLock(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acquire: %v\n", err)
		os.Exit(3)
	}
	switch mode {
	case "increment":
		counter := filepath.Join(dir, "counter")
		data, _ := os.ReadFile(counter)
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(150 * time.Millisecond) // widen the lost-update window
		if err := os.WriteFile(counter, []byte(strconv.Itoa(n+1)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write counter: %v\n", err)
			os.Exit(4)
		}
	case "hold":
		if err := os.WriteFile(filepath.Join(dir, "held"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write held marker: %v\n", err)
			os.Exit(4)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	release()
	os.Exit(0)
}

// lockHelper is one re-exec'd helper process and its captured output.
type lockHelper struct {
	cmd    *exec.Cmd
	output *lockTestBuffer
	done   chan struct{}
	err    error
}

func startLockHelper(t *testing.T, dir, mode string) *lockHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGenerateLockHelperProcess$")
	cmd.Env = append(os.Environ(), generateLockHelperEnv+"="+mode, generateLockHelperDirEnv+"="+dir)
	out := &lockTestBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	h := &lockHelper{cmd: cmd, output: out, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		select {
		case <-h.done:
		default:
			_ = cmd.Process.Kill()
			<-h.done
		}
	})
	return h
}

func (h *lockHelper) wait(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(within):
		t.Fatalf("helper pid %d did not exit within %s; output:\n%s", h.cmd.Process.Pid, within, h.output.String())
	}
}

func (h *lockHelper) exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// lockTestBuffer is a bytes.Buffer safe to read while a subprocess writes it.
type lockTestBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockTestBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockTestBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLockCondition polls cond until it holds or the deadline passes.
func waitForLockCondition(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestGenerateLock_ConcurrentRunsSerialize is the regression test for X6:
// several forge runs started at once in one project must ALL complete, one
// at a time. The lock used to be an O_EXCL marker file, so every run but the
// first failed instantly with "another forge process is running ... remove
// it with: rm .forge/forge.lock" — advice that, once followed, let two
// pipelines interleave their writes and rollbacks.
func TestGenerateLock_ConcurrentRunsSerialize(t *testing.T) {
	dir := t.TempDir()
	const runs = 4
	helpers := make([]*lockHelper, runs)
	for i := range helpers {
		helpers[i] = startLockHelper(t, dir, "increment")
	}
	for i, h := range helpers {
		h.wait(t, 30*time.Second)
		if h.err != nil {
			t.Errorf("run %d (pid %d) failed instead of waiting its turn: %v\n%s", i, h.cmd.Process.Pid, h.err, h.output.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "counter"))
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(runs) {
		t.Errorf("counter = %s, want %d: runs overlapped inside the lock (lost updates) or did not all run", got, runs)
	}
}

// TestGenerateLock_WaiterNamesHolderAndBlocks pins the waiting run's UX: it
// stays alive (blocked, not failed), says so in one line naming the holder's
// pid, and proceeds as soon as the holder releases.
func TestGenerateLock_WaiterNamesHolderAndBlocks(t *testing.T) {
	dir := t.TempDir()
	holder := startLockHelper(t, dir, "hold")
	heldMarker := filepath.Join(dir, "held")
	if !waitForLockCondition(15*time.Second, func() bool { _, err := os.Stat(heldMarker); return err == nil }) {
		t.Fatalf("holder never took the lock; output:\n%s", holder.output.String())
	}
	holderPID := strconv.Itoa(holder.cmd.Process.Pid)

	waiter := startLockHelper(t, dir, "increment")
	if !waitForLockCondition(15*time.Second, func() bool { return strings.Contains(waiter.output.String(), "waiting") || waiter.exited() }) {
		t.Fatalf("waiter printed no waiting notice; output:\n%s", waiter.output.String())
	}
	if waiter.exited() {
		t.Fatalf("waiter exited while the lock was held (err %v) instead of waiting; output:\n%s", waiter.err, waiter.output.String())
	}
	notice := waiter.output.String()
	if !strings.Contains(notice, "pid "+holderPID) {
		t.Errorf("waiting notice does not name the holder pid %s:\n%s", holderPID, notice)
	}
	if strings.Count(strings.TrimSpace(notice), "\n") != 0 {
		t.Errorf("waiting notice should be exactly one line, got:\n%s", notice)
	}

	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	holder.wait(t, 15*time.Second)
	waiter.wait(t, 15*time.Second)
	if waiter.err != nil {
		t.Fatalf("waiter failed after the holder released: %v\n%s", waiter.err, waiter.output.String())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "counter")); strings.TrimSpace(string(data)) != "1" {
		t.Errorf("waiter's critical section did not run after the release (counter %q)", data)
	}
}

// TestGenerateLock_KilledHolderReleasesLock: a holder that dies without
// running its release (SIGKILL, a crashed daemon) must not strand the lock.
// The OS drops the lock with the process, so the next run starts at once —
// no staleness window, no `rm` by hand.
func TestGenerateLock_KilledHolderReleasesLock(t *testing.T) {
	dir := t.TempDir()
	holder := startLockHelper(t, dir, "hold")
	if !waitForLockCondition(15*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "held")); return err == nil }) {
		t.Fatalf("holder never took the lock; output:\n%s", holder.output.String())
	}
	if err := holder.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	holder.wait(t, 15*time.Second)

	acquired := make(chan error, 1)
	go func() {
		release, err := acquireGenerateLock(dir)
		if err == nil {
			release()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("acquire after the holder was killed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("acquire still blocked 10s after the holder was killed: the lock outlived its process")
	}
}

// TestGenerateLock_LeftoverLockFileDoesNotBlock: the lock is the OS lock, not
// the file's existence. A lock file left behind — by an older forge that used
// O_EXCL marker files, or by any earlier run, whatever pid it records — must
// not block a run when no live process holds the lock.
func TestGenerateLock_LeftoverLockFileDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".forge", "forge.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Our own pid is, by definition, a live process.
	body := fmt.Sprintf("pid=%d\ntime=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(lockPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	release, err := acquireGenerateLock(dir)
	if err != nil {
		t.Fatalf("a leftover lock file with no live holder blocked the run: %v", err)
	}
	release()
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("release must leave the lock file in place (deleting it would let a waiter and a newcomer lock different inodes): %v", err)
	}
}

// TestGenerateLock_InProcessSecondAcquireWaits: forge also runs embedded in a
// long-lived host process, so two acquisitions from one process must exclude
// each other just as two processes do — the second waits, it does not fail
// and it does not slip through.
func TestGenerateLock_InProcessSecondAcquireWaits(t *testing.T) {
	dir := t.TempDir()
	releaseFirst, err := acquireGenerateLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	type result struct {
		release func()
		err     error
	}
	second := make(chan result, 1)
	go func() {
		release, err := acquireGenerateLock(dir)
		second <- result{release, err}
	}()
	select {
	case r := <-second:
		if r.release != nil {
			r.release()
		}
		releaseFirst()
		t.Fatalf("second acquire returned while the first was held (err %v); it must wait", r.err)
	case <-time.After(300 * time.Millisecond):
	}
	releaseFirst()
	select {
	case r := <-second:
		if r.err != nil {
			t.Fatalf("second acquire after release: %v", r.err)
		}
		r.release()
	case <-time.After(10 * time.Second):
		t.Fatal("second acquire still blocked 10s after the first released")
	}
}

// TestScaffoldProjectLockIsTheGenerateLock: the lock the scaffold group holds
// for each command (factory GenAPI.HoldProjectLock) must be the SAME lock
// `forge generate` takes, or a scaffold and a generate would not exclude
// each other at all.
func TestScaffoldProjectLockIsTheGenerateLock(t *testing.T) {
	hold := factory.New().Gen.HoldProjectLock
	if hold == nil {
		t.Fatal("GenAPI.HoldProjectLock is not wired: scaffold commands would run unlocked")
	}
	dir := t.TempDir()
	releaseGenerate, err := acquireGenerateLock(dir)
	if err != nil {
		t.Fatalf("hold the generate lock: %v", err)
	}
	type result struct {
		release func()
		err     error
	}
	scaffold := make(chan result, 1)
	go func() {
		release, err := hold(dir)
		scaffold <- result{release, err}
	}()
	select {
	case r := <-scaffold:
		if r.release != nil {
			r.release()
		}
		releaseGenerate()
		t.Fatalf("scaffold's project lock was granted while generate held its lock (err %v)", r.err)
	case <-time.After(300 * time.Millisecond):
	}
	releaseGenerate()
	select {
	case r := <-scaffold:
		if r.err != nil {
			t.Fatalf("scaffold's project lock after generate released: %v", r.err)
		}
		r.release()
	case <-time.After(10 * time.Second):
		t.Fatal("scaffold's project lock still blocked 10s after generate released")
	}
}

// TestGenerateCmd_WaitsForConcurrentRun drives the real `forge generate`
// command while another run holds the project's lock: it must wait, then
// proceed — not fail with "another forge process is running". The project is
// empty, so once it gets the lock the pipeline stops at its first step; only
// the timing and the error text matter here.
func TestGenerateCmd_WaitsForConcurrentRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	release, err := acquireGenerateLock(dir)
	if err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	cmd := newGenerateCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		release()
		t.Fatalf("forge generate returned while another run held the lock (err: %v); it must wait its turn", err)
	case <-time.After(500 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil && strings.Contains(err.Error(), "another forge process") {
			t.Fatalf("forge generate still reports a lock conflict after the holder released: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("forge generate did not proceed within 60s of the lock being released")
	}
}
