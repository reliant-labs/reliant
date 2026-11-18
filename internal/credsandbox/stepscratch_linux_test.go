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
// ends, whichever way it ends, and the sweep removes what an earlier daemon
// left once nothing holds it, never what a live step still uses.

// stepRoot is a credentialed scratch root of the test's own, with the reclaim's
// waits shortened and no sweep pending from an earlier test.
func stepRoot(t *testing.T) string {
	t.Helper()
	tries, every, age := stepScratchSettleTries, stepScratchSettleEvery, stepScratchMinAge
	stepScratchSettleTries, stepScratchSettleEvery = 200, 10*time.Millisecond
	stepSweep.Lock()
	last := stepSweep.last
	stepSweep.last = time.Now() // makeStepScratch starts no sweep of its own
	stepSweep.Unlock()
	t.Cleanup(func() {
		stepScratchSettleTries, stepScratchSettleEvery, stepScratchMinAge = tries, every, age
		stepSweep.Lock()
		stepSweep.last = last
		stepSweep.Unlock()
	})
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
// job it left running) keeps it until that process is gone too.
func TestStepScratch_DoneSparesAScratchALeftoverProcessStillHolds(t *testing.T) {
	root := stepRoot(t)
	stepScratchSettleTries = 3
	cmd, s := stepCommand(t, context.Background(), root, `true`)
	require.NoError(t, cmd.Run())
	proc := fakeProc(t, map[int]fakeProcess{4242: {env: []string{StepScratchEnv + "=" + s.Dir()}}})
	stepScratchProcRoot = proc
	t.Cleanup(func() { stepScratchProcRoot = "/proc" })

	s.reclaim(nil)
	assert.DirExists(t, s.Dir(), "a live process holds it in its environment")

	require.NoError(t, os.RemoveAll(filepath.Join(proc, "4242")))
	s.reclaim(nil)
	assert.NoDirExists(t, s.Dir(), "gone once nothing holds it")
}

// fakeProcess is what a fake /proc shows for one process.
type fakeProcess struct {
	env          []string
	cwd          string
	fds          []string
	unreadableFD bool
	// unreadableEnv withholds its environment, as the kernel does from
	// another user namespace's process; nsDepth is its pid namespace depth
	// (NSpid), 1 being ours.
	unreadableEnv bool
	nsDepth       int
}

// fakeProc builds a /proc of the test's own: environ, cwd, fd and status for
// each process, all this user's, and a self in the first pid namespace.
func fakeProc(t *testing.T, procs map[int]fakeProcess) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "self", "status"), []byte("Name:\ttest\nNSpid:\t4000\n"), 0o600))
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "fd"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(p.env, "\x00")+"\x00"), 0o600))
		cwd := p.cwd
		if cwd == "" {
			cwd = "/"
		}
		require.NoError(t, os.Symlink(cwd, filepath.Join(dir, "cwd")))
		for i, f := range p.fds {
			require.NoError(t, os.Symlink(f, filepath.Join(dir, "fd", strconv.Itoa(i))))
		}
		if p.unreadableFD {
			require.NoError(t, os.Chmod(filepath.Join(dir, "fd"), 0))
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "fd"), 0o700) })
		}
		if p.unreadableEnv {
			require.NoError(t, os.Chmod(filepath.Join(dir, "environ"), 0))
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

// stepDir makes a command scratch directory in root as if made age ago.
func stepDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "work"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "work", "f"), []byte("x"), 0o600))
	at := time.Now().Add(-age)
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
	young := stepDir(t, root, "step-999999-young", 0)
	legacy := stepDir(t, root, "tmp.AbCdEf1234", time.Hour)
	slots := stepDir(t, root, "slots", time.Hour)

	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{
		10: {env: []string{"PATH=/bin", StepScratchEnv + "=" + byEnv}},
		11: {cwd: filepath.Join(byCwd, "work")},
		12: {fds: []string{"/dev/null", filepath.Join(byFD, "work", "f")}},
		// Seen through another mount of the disk: matched by name.
		13: {env: []string{"TMPDIR=/mnt/workspace-disk/home/u/.reliant/cred-scratch/step-999999-tmpdir/t"}},
	})
	t.Cleanup(func() { stepScratchProcRoot = "/proc" })

	assert.Equal(t, 1, sweepStepScratch(root))
	assert.NoDirExists(t, dead, "nothing holds it: its daemon and its command are gone")
	for _, d := range []string{byEnv, byCwd, byFD, byTmpdir} {
		assert.DirExists(t, d, "a live step's directory is spared")
	}
	assert.DirExists(t, young, "too young to judge: its command may not have started")
	assert.DirExists(t, legacy, "a directory a step made itself is not the daemon's to judge")
	assert.DirExists(t, slots, "nor is anything else in the root")
}

// An incomplete picture of the live processes proves nothing: nothing goes.
func TestSweepStepScratch_RemovesNothingWhenAProcessCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory anyway")
	}
	root := stepRoot(t)
	dead := stepDir(t, root, "step-999999-dead", time.Hour)
	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{20: {unreadableFD: true}})
	t.Cleanup(func() { stepScratchProcRoot = "/proc" })

	assert.Equal(t, 0, sweepStepScratch(root))
	assert.DirExists(t, dead)
}

// A container whose processes share this user's uid (k3d's coredns, on a cloud
// workspace) withholds them from it: one in a pid namespace below ours cannot
// hold a daemon command's scratch and is skipped, while a withheld process in
// our own namespace still makes the picture incomplete.
func TestSweepStepScratch_SkipsAContainersWithheldProcess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file anyway")
	}
	root := stepRoot(t)
	dead := stepDir(t, root, "step-999999-dead", time.Hour)
	t.Cleanup(func() { stepScratchProcRoot = "/proc" })

	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{30: {unreadableEnv: true, nsDepth: 1}})
	assert.Equal(t, 0, sweepStepScratch(root), "withheld in our own namespace: nothing is proven")
	assert.DirExists(t, dead)

	stepScratchProcRoot = fakeProc(t, map[int]fakeProcess{31: {unreadableEnv: true, nsDepth: 3}})
	assert.Equal(t, 1, sweepStepScratch(root))
	assert.NoDirExists(t, dead)
}

// The same against this machine's real /proc: a live step's scratch, held only
// by its environment, outlives its daemon until the step is gone.
func TestSweepStepScratch_SparesALiveStepsDirOnThisMachine(t *testing.T) {
	root := stepRoot(t)
	if _, err := stepScratchRefs("/proc"); err != nil {
		t.Skipf("this machine's /proc cannot be read completely as this user (%v), so the sweep proves nothing here", err)
	}
	cmd, s := stepCommand(t, context.Background(), root, `exec sleep 60`)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	stepScratchMinAge = 0

	sweepStepScratch(root)
	assert.DirExists(t, s.Dir(), "its step is running")

	require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
	_ = cmd.Wait()
	// Another process of this machine caught mid-exec makes one scan
	// incomplete, which spares everything; the next proves it.
	require.Eventually(t, func() bool { sweepStepScratch(root); _, err := os.Stat(s.Dir()); return errors.Is(err, os.ErrNotExist) },
		10*time.Second, 50*time.Millisecond, "its step is gone")
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
