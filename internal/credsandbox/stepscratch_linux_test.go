// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"context"
	"errors"
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
	tries, every, linger, age, now, proc := stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery, stepScratchMinAge, stepScratchNow, stepScratchProcRoot
	stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery = 200, 10*time.Millisecond, 10*time.Millisecond
	stepScratchNow = func() time.Time { return testNow }
	stepSweep.Lock()
	last := stepSweep.last
	stepSweep.last = testNow
	stepSweep.Unlock()
	t.Cleanup(func() {
		stepScratchSettleTries, stepScratchSettleEvery, stepScratchLingerEvery, stepScratchMinAge, stepScratchNow, stepScratchProcRoot = tries, every, linger, age, now, proc
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

// The startup sweep: an earlier daemon's commands' scratch directories go once
// nothing holds them, and a live step's stays, however it holds it.
func TestSweepStepScratch_RemovesOrphansAndSparesALiveStepsDir(t *testing.T) {
	root := stepRoot(t)
	dead := stepDir(t, root, "step-999999-dead", time.Hour)
	byEnv := stepDir(t, root, "step-999999-env", time.Hour)
	byCwd := stepDir(t, root, "step-999999-cwd", time.Hour)
	byFD := stepDir(t, root, "step-999999-fd", time.Hour)
	byTmpdir := stepDir(t, root, "step-999999-tmpdir", time.Hour)
	young := stepDir(t, root, "step-999999-young", stepScratchMinAge-time.Second)
	legacy := stepDir(t, root, "tmp.AbCdEf1234", time.Hour)
	slots := stepDir(t, root, "slots", time.Hour)

	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{
		10: {env: []string{"PATH=/bin", StepScratchEnv + "=" + byEnv}},
		11: {cwd: filepath.Join(byCwd, "work")},
		12: {fds: []string{"/dev/null", filepath.Join(byFD, "work", "f")}},
		// Seen through another mount of the disk: matched by name.
		13: {env: []string{"TMPDIR=/mnt/workspace-disk/home/u/.reliant/cred-scratch/step-999999-tmpdir/t"}},
	})

	assert.Equal(t, 1, sweepStepScratch(root))
	assert.NoDirExists(t, dead, "nothing holds it: its daemon and its command are gone")
	for _, d := range []string{byEnv, byCwd, byFD, byTmpdir} {
		assert.DirExists(t, d, "a live step's directory is spared")
	}
	assert.DirExists(t, young, "too young to judge: another daemon's command may not have started")
	assert.DirExists(t, legacy, "a directory a step made itself is not the daemon's to judge")
	assert.DirExists(t, slots, "nor is anything else in the root")
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
	orphan := stepDir(t, root, "step-999999-orphan", time.Hour)

	assert.Equal(t, 1, sweepStepScratch(root))
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
	orphan := stepDir(t, root, "step-999999-orphan", time.Hour)
	var made string
	stepSweepListed = func() {
		var err error
		made, err = makeStepScratch(root)
		require.NoError(t, err)
		old := testNow.Add(-time.Hour)
		require.NoError(t, os.Chtimes(made, old, old))
	}

	assert.Equal(t, 1, sweepStepScratch(root))
	require.NotEmpty(t, made)
	assert.DirExists(t, made, "made after the listing: never a candidate")
	assert.NoDirExists(t, orphan)
	assert.Equal(t, 0, sweepStepScratch(root), "nor in the next sweep: it is registered")
	assert.DirExists(t, made)
}

// An incomplete picture of the live processes proves nothing: nothing goes,
// whichever read failed and wherever the process runs.
func TestSweepStepScratch_RemovesNothingWhenAProcessCannotBeRead(t *testing.T) {
	for name, p := range map[string]fakeProcess{
		"descriptors": {unreadableFD: true},
		"environment": {unreadableEnv: true},
		// A container's process sharing our uid, withheld from us: nothing
		// proves it was not handed a descriptor or a bind mount of one.
		"environment, in a nested pid namespace": {unreadableEnv: true, nsDepth: 3},
	} {
		t.Run(name, func(t *testing.T) {
			root := stepRoot(t)
			dead := stepDir(t, root, "step-999999-dead", time.Hour)
			stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{20: p})

			assert.Equal(t, 0, sweepStepScratch(root))
			assert.DirExists(t, dead)
			_, err := stepScratchRefs(stepScratchProcRoot)
			assert.Error(t, err, "the scan reports itself incomplete")
		})
	}
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
