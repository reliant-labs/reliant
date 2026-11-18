// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"context"
	"errors"
	"io/fs"
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
	"golang.org/x/sys/unix"
)

// A credentialed command's own scratch (stepscratch.go) goes when the command
// ends, whichever way it ends; the sweep removes only what an earlier daemon
// left once nothing provably holds it, never a directory this daemon handed
// out, and nothing at all when a process's holdings cannot be read.

// testNow is the sweep's clock in these tests: ages are judged against it, so
// no assertion depends on when the test runs.
var testNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// stepRoot is a credentialed scratch root of the test's own, with the reclaim's
// waits shortened, the clock fixed at testNow, an empty /proc, and no sweep
// due (makeStepScratch starts none of its own).
func stepRoot(t *testing.T) string {
	t.Helper()
	tries, every, linger, now, proc := stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery, stepScratchNow, stepScratchProcRoot
	stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery = 200, 10*time.Millisecond, 10*time.Millisecond
	stepScratchNow = func() time.Time { return testNow }
	stepSweep.Lock()
	last := stepSweep.last
	stepSweep.last = testNow
	stepSweep.Unlock()
	fsys := procFS
	t.Cleanup(func() {
		procFS = fsys
		stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery, stepScratchNow, stepScratchProcRoot = tries, every, linger, now, proc
		stepSweep.Lock()
		stepSweep.last = last
		stepSweep.Unlock()
		stepSweepListed = func() {}
	})
	stepScratchProcRoot = fakeProc(t, nil)
	root := filepath.Join(t.TempDir(), "cred-scratch")
	require.NoError(t, os.Mkdir(root, 0o700))
	return root
}

// stepCommand makes a command's scratch in root, with a file in it the way a
// step's build would leave one, and sh running as the command's own process
// group with the scratch in its environment, as wrap starts a credentialed one.
func stepCommand(t *testing.T, ctx context.Context, root, sh string) (*exec.Cmd, *StepScratch) {
	t.Helper()
	dir, err := makeStepScratch(root)
	require.NoError(t, err)
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	assert.True(t, strings.HasPrefix(filepath.Base(dir), stepScratchPrefix+strconv.Itoa(os.Getpid())+"-"), dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "go", "cache"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go", "cache", "big"), make([]byte, 4096), 0o600))
	cmd := exec.CommandContext(ctx, "sh", "-c", sh)
	cmd.Env = []string{"PATH=/usr/bin:/bin", StepScratchEnv + "=" + dir, "TMPDIR=" + dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// What the daemon's timeout and cancel do in the end: the whole group, with
	// no chance to run a trap.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd, &StepScratch{dir: dir}
}

// groupGone is proctree's cgroup proof for a command without one: no process
// of its group is left.
func groupGone(pgid int) func() bool {
	return func() bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }
}

// trappedStep is a step the way the pipeline's bootstrap writes one: an EXIT
// trap that removes its scratch, then work (a builtin read, so the shell is
// the command's only process). It says READY once the trap is set.
const trappedStep = `trap 'rm -rf "$` + StepScratchEnv + `"' EXIT; echo READY; read x`

// startTrapped starts cmd with its stdin held open and waits for READY.
func startTrapped(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	buf := make([]byte, len("READY\n"))
	_, err = stdout.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "READY\n", string(buf))
}

func TestStepScratch_AKilledCommandsScratchIsRemoved(t *testing.T) {
	root := stepRoot(t)
	cmd, s := stepCommand(t, context.Background(), root, trappedStep)
	startTrapped(t, cmd)
	pgid := cmd.Process.Pid
	require.NoError(t, syscall.Kill(-pgid, syscall.SIGKILL))
	_ = cmd.Wait()
	assert.DirExists(t, s.Dir(), "a SIGKILL never runs the step's own trap")

	s.reclaim(groupGone(pgid))
	assert.NoDirExists(t, s.Dir(), "the daemon removes a killed command's scratch")
}

// A node timeout and a cancelled run end a command the same way: its context
// ends and the daemon kills its group. The timer starts once the step is
// running, so a loaded machine cannot end it before its trap is set.
func TestStepScratch_ATimedOutCommandsScratchIsRemoved(t *testing.T) {
	root := stepRoot(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cmd, s := stepCommand(t, ctx, root, trappedStep)
	startTrapped(t, cmd)
	pgid := cmd.Process.Pid
	time.AfterFunc(100*time.Millisecond, func() { cancel(context.DeadlineExceeded) })
	err := cmd.Wait()
	require.Error(t, err, "the timeout kills it")
	require.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
	assert.DirExists(t, s.Dir(), "killed by the timeout, its own trap did not remove it")

	s.Done(groupGone(pgid))
	require.Eventually(t, func() bool { _, err := os.Stat(s.Dir()); return errors.Is(err, os.ErrNotExist) },
		10*time.Second, 10*time.Millisecond, "Done removes it in the background")
}

// A command that ends with a process still holding its scratch (a background
// job it left running) keeps it, registered, until that process is gone too.
func TestStepScratch_DoneSparesAScratchALeftoverProcessStillHolds(t *testing.T) {
	root := stepRoot(t)
	cmd, s := stepCommand(t, context.Background(), root, `true`)
	require.NoError(t, cmd.Run())
	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{4242: {env: []string{StepScratchEnv + "=" + s.Dir()}}})

	assert.False(t, s.tryReclaim(nil))
	assert.DirExists(t, s.Dir(), "a live process holds it in its environment")
	assert.True(t, stepScratchRegistered(s.Dir()), "still this daemon's: no sweep may take it")

	stepScratchProcRoot = fakeProc(t, nil)
	assert.True(t, s.tryReclaim(nil))
	assert.NoDirExists(t, s.Dir(), "gone once nothing holds it")
	assert.False(t, stepScratchRegistered(s.Dir()), "its own removal is what takes it out of the registry")
}

// fakeProcess is what a fake /proc shows for one process.
type fakeProcess struct {
	env []string
	cwd string
	fds []string
	// unreadableEnv and unreadableFD make its environment or descriptors
	// fail to read for a reason other than its exit (here a wrong file type,
	// which fails the same for root as for anyone).
	unreadableEnv, unreadableFD bool
	// nsDepth, when set, is its pid namespace depth (NSpid), 1 being ours.
	nsDepth int
}

// fakeProc builds a /proc of the test's own: environ, cwd, fd and status for
// each process, all this user's.
func fakeProc(t *testing.T, procs map[int]fakeProcess) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "self", "status"), []byte("NSpid:\t4000\n"), 0o600))
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(dir, 0o700))
		if p.unreadableEnv {
			require.NoError(t, os.Mkdir(filepath.Join(dir, "environ"), 0o700))
		} else {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(p.env, "\x00")+"\x00"), 0o600))
		}
		cwd := p.cwd
		if cwd == "" {
			cwd = "/"
		}
		require.NoError(t, os.Symlink(cwd, filepath.Join(dir, "cwd")))
		if p.unreadableFD {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "fd"), nil, 0o600))
		} else {
			require.NoError(t, os.Mkdir(filepath.Join(dir, "fd"), 0o700))
		}
		for i, f := range p.fds {
			require.NoError(t, os.Symlink(f, filepath.Join(dir, "fd", strconv.Itoa(i))))
		}
		if p.nsDepth > 0 {
			ids := make([]string, p.nsDepth)
			for i := range ids {
				ids[i] = strconv.Itoa(pid + i)
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "status"), []byte("NSpid:\t"+strings.Join(ids, "\t")+"\n"), 0o600))
		}
	}
	return root
}

// stepDir makes a scratch directory in root as an earlier daemon would have
// left it, last changed age before testNow.
func stepDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "work"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "work", "f"), []byte("x"), 0o600))
	at := testNow.Add(-age)
	require.NoError(t, os.Chtimes(dir, at, at))
	return dir
}

// The race the registry closes: a directory this daemon handed out whose
// command has not started yet is held by no process, and may be old enough to
// judge (a slow start, a clock step). The sweep never takes it; only its own
// removal does.
func TestSweepStepScratch_NeverTakesADirectoryThisDaemonHandedOut(t *testing.T) {
	root := stepRoot(t)
	dir, err := makeStepScratch(root)
	require.NoError(t, err)
	old := testNow.Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, old, old))
	orphan := releasedOrphan(t, root)

	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.DirExists(t, dir, "registered: its command is about to start")
	assert.NoDirExists(t, orphan)

	assert.True(t, (&StepScratch{dir: dir}).tryReclaim(nil))
	assert.NoDirExists(t, dir)
	assert.False(t, stepScratchRegistered(dir))
}

// A directory made while a sweep runs, between its listing of the root and
// its scan of /proc, is never one it judges.
func TestSweepStepScratch_ADirectoryMadeDuringTheSweepSurvivesIt(t *testing.T) {
	root := stepRoot(t)
	orphan := releasedOrphan(t, root)
	var made string
	stepSweepListed = func() {
		var err error
		made, err = makeStepScratch(root)
		require.NoError(t, err)
		old := testNow.Add(-time.Hour)
		require.NoError(t, os.Chtimes(made, old, old))
	}

	assert.Equal(t, 1, sweepRemoved(t, root))
	require.NotEmpty(t, made)
	assert.DirExists(t, made, "made after the listing: never a candidate")
	assert.NoDirExists(t, orphan)
	assert.Equal(t, 0, sweepRemoved(t, root), "nor in the next sweep: it is registered")
	assert.DirExists(t, made)
}

// Done's own reclaim fails closed the same way: a scratch whose command has no
// cgroup to prove it gone stays while the processes cannot all be read.
func TestStepScratch_DoneKeepsItWhileAProcessCannotBeRead(t *testing.T) {
	root := stepRoot(t)
	cmd, s := stepCommand(t, context.Background(), root, `true`)
	require.NoError(t, cmd.Run())
	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{21: {unreadableEnv: true, nsDepth: 3}})

	assert.False(t, s.tryReclaim(nil))
	assert.DirExists(t, s.Dir())
	assert.True(t, s.tryReclaim(func() bool { return true }), "a proven-empty cgroup needs no scan")
	assert.NoDirExists(t, s.Dir())
}

// The real /proc of a live process of ours: what it holds in its environment,
// as its working directory and through an open descriptor is what the scan
// reads.
func TestStepScratchRefs_ReadsALiveProcess(t *testing.T) {
	root := stepRoot(t)
	byEnv := stepDir(t, root, "step-1-env", time.Hour)
	byCwd := stepDir(t, root, "step-1-cwd", time.Hour)
	byFD := stepDir(t, root, "step-1-fd", time.Hour)
	f, err := os.Open(filepath.Join(byFD, "work", "f"))
	require.NoError(t, err)
	defer f.Close()

	cmd := exec.Command("sh", "-c", `echo READY; read x`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", StepScratchEnv + "=" + byEnv}
	cmd.Dir = filepath.Join(byCwd, "work")
	cmd.ExtraFiles = []*os.File{f}
	// READY comes after the exec, so the environment /proc shows is its own.
	startTrapped(t, cmd)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	refs := map[string]bool{}
	require.NoError(t, readStepScratchRefs(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid)), refs))
	assert.Equal(t, map[string]bool{"step-1-env": true, "step-1-cwd": true, "step-1-fd": true}, refs)
}

// A command Wrap gave a scratch that never reached a tree (a return before
// proctree.Prepare) does not leak it: Release, which every exec path runs,
// reclaims it.
func TestRelease_ReclaimsAScratchNoTreeTook(t *testing.T) {
	root := stepRoot(t)
	dir, err := makeStepScratch(root)
	require.NoError(t, err)
	cmd := exec.Command("true")
	stepScratches.Store(cmd, &StepScratch{dir: dir})

	Release(cmd)
	require.Eventually(t, func() bool { return !stepScratchRegistered(dir) }, 10*time.Second, 10*time.Millisecond)
	assert.NoDirExists(t, dir)
	assert.Nil(t, TakeStepScratch(cmd))
}

// sweepRemoved runs a sweep and returns how many directories it removed.
func sweepRemoved(t *testing.T, root string) int {
	t.Helper()
	return sweepStepScratch(root)
}

// failRead makes reads of path, under the fake /proc, fail with err until the
// returned func is called: what the kernel does to a process mid-exit (ESRCH),
// or to one whose pid was taken over between the listing and the read.
func failRead(t *testing.T, path string, err error) (heal func()) {
	t.Helper()
	failing := true
	fsys := procFS
	wrap := func(p string) error {
		if failing && p == path {
			return &fs.PathError{Op: "read", Path: p, Err: err}
		}
		return nil
	}
	procFS.readFile = func(p string) ([]byte, error) {
		if e := wrap(p); e != nil {
			return nil, e
		}
		return fsys.readFile(p)
	}
	procFS.readlink = func(p string) (string, error) {
		if e := wrap(p); e != nil {
			return "", e
		}
		return fsys.readlink(p)
	}
	procFS.readDir = func(p string) ([]os.DirEntry, error) {
		if e := wrap(p); e != nil {
			return nil, e
		}
		return fsys.readDir(p)
	}
	procFS.lstat = func(p string) (os.FileInfo, error) {
		if e := wrap(p); e != nil {
			return nil, e
		}
		return fsys.lstat(p)
	}
	return func() { failing = false }
}

// Done, for a command whose cgroup proves it gone, removes its scratch without
// reading /proc at all, so an ESRCH there cannot hold it back or decide it.
func TestStepScratch_DoneWithAnEmptyCgroupNeverReadsProc(t *testing.T) {
	root := stepRoot(t)
	cmd, s := stepCommand(t, context.Background(), root, `true`)
	require.NoError(t, cmd.Run())
	read := false
	procFS.readDir = func(string) ([]os.DirEntry, error) { read = true; return nil, syscall.ESRCH }
	procFS.lstat = func(string) (os.FileInfo, error) { read = true; return nil, syscall.ESRCH }

	assert.True(t, s.tryReclaim(func() bool { return true }))
	assert.NoDirExists(t, s.Dir())
	assert.False(t, read, "the cgroup is the proof; /proc is not read")

	// Without a cgroup, the same ESRCH is no proof: it stays.
	_, s2 := stepCommand(t, context.Background(), root, `true`)
	assert.False(t, s2.tryReclaim(nil))
	assert.DirExists(t, s2.Dir())
}

// orphanWithLock is a scratch directory another daemon made, with its lock
// file, aged an hour: owned while the returned lock is open, an orphan once it
// is closed. It is not in this daemon's registry.
func orphanWithLock(t *testing.T, root string) (string, *os.File) {
	t.Helper()
	dir, lock, err := newStepScratch(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	assert.False(t, stepScratchRegistered(dir), "another daemon's: not in this registry")
	old := testNow.Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, old, old))
	return dir, lock
}

// takeLock takes dir's ownership lock through a descriptor of its own, as its
// owning daemon holds it.
func takeLock(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, stepScratchLockName), os.O_RDWR, 0)
	require.NoError(t, err)
	require.NoError(t, unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// A daemon's directory is locked from the moment its step- name exists, and
// the lock is held by the daemon alone: no command it starts inherits it.
func TestMakeStepScratch_TheDaemonHoldsTheLockAndNoCommandInheritsIt(t *testing.T) {
	root := stepRoot(t)
	dir, err := makeStepScratch(root)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, stepScratchLockName))
	_, state, err := tryLockStepScratch(dir)
	require.NoError(t, err)
	assert.Equal(t, lockOwned, state, "the daemon holds it")
	leftovers, _ := filepath.Glob(filepath.Join(root, stepScratchNewPrefix+"*"))
	assert.Empty(t, leftovers, "the .new- name is gone once the step- name exists")

	cmd := exec.Command("sh", "-c", `echo READY; read x`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", StepScratchEnv + "=" + dir}
	startTrapped(t, cmd)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	fds, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd"))
	require.NoError(t, err)
	for _, fd := range fds {
		target, _ := os.Readlink(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd", fd.Name()))
		assert.NotEqual(t, filepath.Join(dir, stepScratchLockName), target, "descriptor %s of the command is the lock", fd.Name())
	}

	assert.True(t, (&StepScratch{dir: dir}).tryReclaim(func() bool { return true }))
	assert.NoDirExists(t, dir)
}

// Another daemon on the same home (a registry of its own) owns its directory:
// nothing in /proc names it and it is old, but its lock is held, so the sweep
// keeps it. Released, the next sweep takes it.
func TestSweepStepScratch_SparesADirectoryAnotherDaemonOwns(t *testing.T) {
	root := stepRoot(t)
	dir, lock := orphanWithLock(t, root)

	assert.Equal(t, 0, sweepRemoved(t, root))
	assert.DirExists(t, dir, "its daemon holds its lock")

	require.NoError(t, lock.Close())
	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.NoDirExists(t, dir)
	trash, _ := filepath.Glob(filepath.Join(root, stepScratchTrashPrefix+"*"))
	assert.Empty(t, trash, "moved aside, then removed")
}

// The kernel drops a flock when the last descriptor of it closes, however the
// process holding it exits: a SIGKILLed daemon leaves its directories
// unlocked, and the next sweep takes them.
func TestSweepStepScratch_TakesADirectoryOnceItsOwningDaemonDies(t *testing.T) {
	root := stepRoot(t)
	dir, lock := orphanWithLock(t, root)
	// The "daemon" is a process of its own that holds the lock's descriptor.
	daemon := exec.Command("sh", "-c", `echo READY; read x`)
	daemon.ExtraFiles = []*os.File{lock}
	startTrapped(t, daemon)
	require.NoError(t, lock.Close(), "only the daemon process holds it now")
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })

	assert.Equal(t, 0, sweepRemoved(t, root))
	assert.DirExists(t, dir, "its daemon is alive")

	require.NoError(t, daemon.Process.Signal(syscall.SIGKILL))
	_ = daemon.Wait()
	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.NoDirExists(t, dir, "the lock went with its daemon")
}

// A directory whose owner takes its lock after the sweep listed it (a command
// started after the listing, through its daemon) is kept: the lock is tried
// right before the removal, not at the listing.
func TestSweepStepScratch_KeepsADirectoryLockedAfterTheListing(t *testing.T) {
	root := stepRoot(t)
	dir, lock := orphanWithLock(t, root)
	require.NoError(t, lock.Close())
	var owner *os.File
	stepSweepListed = func() { owner = takeLock(t, dir) }

	assert.Equal(t, 0, sweepRemoved(t, root))
	assert.DirExists(t, dir)

	stepSweepListed = func() {}
	require.NoError(t, owner.Close())
	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.NoDirExists(t, dir)
}

// releasedOrphan is a scratch directory whose creating daemon is gone: made
// with its lock, which is then closed.
func releasedOrphan(t *testing.T, root string) string {
	t.Helper()
	dir, lock := orphanWithLock(t, root)
	require.NoError(t, lock.Close())
	return dir
}

// The sweep removes only what it can prove no daemon can hand out: a step-
// directory whose lock it takes. A step- directory with no lock file (made
// before locks, or one whose step removed its .lock), the PR pipeline's tmp.*
// directories, slots/ and any other name are never touched, however old and
// whatever /proc says (the sweep does not read it).
func TestSweepStepScratch_RemovesOnlyWhatItCanLock(t *testing.T) {
	root := stepRoot(t)
	orphan := releasedOrphan(t, root)
	noLock := stepDir(t, root, "step-999999-nolock", 100*time.Hour)
	legacy := stepDir(t, root, "tmp.AbCdEf1234", 100*time.Hour)
	slots := stepDir(t, root, "slots", 100*time.Hour)
	other := stepDir(t, root, "other", 100*time.Hour)
	// /proc cannot be read at all: the sweep never needed it.
	stepScratchProcRoot = filepath.Join(t.TempDir(), "no-proc")

	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.NoDirExists(t, orphan, "its lock was free: its daemon is gone")
	for _, d := range []string{noLock, legacy, slots, other} {
		assert.DirExists(t, d, "nothing proves no daemon can hand it out: left to control-plane's janitor")
	}
	trash, _ := filepath.Glob(filepath.Join(root, stepScratchTrashPrefix+"*"))
	assert.Empty(t, trash, "moved aside under the lock, then removed")
}

// A process that inherited a step's scratch (StepScratchEnv) and outlived its
// daemon (one restarted under its running commands) does not keep the
// directory: its daemon is gone, so its step is over, and the sweep that takes
// the free lock removes it. This is the documented, intended behaviour.
func TestSweepStepScratch_RemovesADeadDaemonsDirectoryAnInheritingProcessStillNames(t *testing.T) {
	root := stepRoot(t)
	dir := releasedOrphan(t, root)
	cmd := exec.Command("sh", "-c", `echo READY; read x`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", StepScratchEnv + "=" + dir, "TMPDIR=" + dir}
	startTrapped(t, cmd)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	stepScratchProcRoot = "/proc"

	assert.Equal(t, 1, sweepRemoved(t, root))
	assert.NoDirExists(t, dir)
}

// What a crash left, judged by the same lock: a .trash- directory (a sweep
// stopped mid-removal) and a .new- one (a daemon killed while making it, never
// handed out) go when their lock is free; one being made or removed right now
// (lock held) stays, and so does a .new- one with no lock file yet.
func TestSweepStepScratch_FinishesWhatACrashLeftByItsLock(t *testing.T) {
	root := stepRoot(t)
	withLock := func(name string, held bool) string {
		d := stepDir(t, root, name, time.Hour)
		require.NoError(t, os.WriteFile(filepath.Join(d, stepScratchLockName), nil, 0o600))
		if held {
			takeLock(t, d)
		}
		return d
	}
	trashFree := withLock(stepScratchTrashPrefix+"step-1-a-1", false)
	trashHeld := withLock(stepScratchTrashPrefix+"step-1-b-1", true)
	newFree := withLock(stepScratchNewPrefix+"step-1-c", false)
	newHeld := withLock(stepScratchNewPrefix+"step-1-d", true)
	newNoLock := stepDir(t, root, stepScratchNewPrefix+"step-1-e", time.Hour)

	assert.Equal(t, 2, sweepRemoved(t, root))
	assert.NoDirExists(t, trashFree)
	assert.NoDirExists(t, newFree)
	assert.DirExists(t, trashHeld, "another sweep is removing it")
	assert.DirExists(t, newHeld, "a daemon is making it")
	assert.DirExists(t, newNoLock, "no lock yet: a daemon may be making it")
}

// A command's own reclaim, for a command with no cgroup, reads /proc, and an
// ESRCH (or ENOENT) on any read of a listed process is no proof: it may be
// exiting, its pid may already be another process's, or it may run in another
// pid namespace. The directory stays until a clean walk shows nothing holds it.
func TestStepScratch_DoneWithoutACgroupKeepsItWhileAReadFails(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		err        error
	}{
		{"environment ESRCH", "environ", syscall.ESRCH},
		{"working directory ESRCH", "cwd", syscall.ESRCH},
		{"descriptors ESRCH", "fd", syscall.ESRCH},
		{"one descriptor ESRCH", "fd/0", syscall.ESRCH},
		{"environment ENOENT (the pid went away)", "environ", fs.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := stepRoot(t)
			cmd, s := stepCommand(t, context.Background(), root, `true`)
			require.NoError(t, cmd.Run())
			stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{
				30: {env: []string{"PATH=/bin"}},
				31: {fds: []string{"/dev/null"}},
			})
			heal := failRead(t, filepath.Join(stepScratchProcRoot, "31", tc.file), tc.err)

			assert.False(t, s.tryReclaim(nil))
			assert.DirExists(t, s.Dir())
			_, err := stepScratchRefs(stepScratchProcRoot)
			require.ErrorIs(t, err, tc.err, "the walk reports which read failed")

			heal()
			assert.True(t, s.tryReclaim(nil), "a clean walk with no holder")
			assert.NoDirExists(t, s.Dir())
		})
	}
}

// An inherited StepScratchEnv names another daemon's step's scratch: no
// command of this daemon carries it on.
func TestWithSignal_DropsAnInheritedStepScratch(t *testing.T) {
	env := withSignal([]string{"PATH=/bin", StepScratchEnv + "=/elsewhere/step-1-x", SignalEnv + "=2"}, true)
	assert.Equal(t, []string{"PATH=/bin", SignalEnv + "=" + strconv.Itoa(SignalVersion)}, env)

	cmd := exec.Command("true")
	cmd.Env = withSignal([]string{StepScratchEnv + "=/elsewhere/step-1-x"}, false)
	withStepScratch(cmd, false)
	assert.Empty(t, cmd.Env, "without the signal it gets none")
	assert.Nil(t, TakeStepScratch(cmd))
}
