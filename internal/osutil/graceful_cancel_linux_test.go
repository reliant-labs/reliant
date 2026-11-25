// Copyright (c) 2026 Reliant Labs
//go:build linux

package osutil

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

	"github.com/reliant-labs/reliant/internal/proctree"
)

// strayScript is a command shaped like a workflow run node's: a multi-line
// shell (so bash forks rather than execs) whose work runs under launcher,
// which puts it in a process group of its own (`timeout`, setsid). The work
// writes its pid to pidFile and records, from an EXIT trap, that it was let
// clean up — the trap that releases a lane's claim in rc-train's cp_gate.
// ignoreTERM makes the work refuse SIGTERM instead, for the escalation.
//
// The trap ignores SIGTERM while it runs. GNU timeout sends its child the
// SIGTERM it was sent twice, once to the child and once to its group; when it
// is preempted between the two, the second lands while bash runs its EXIT
// trap and kills it there (about one run in a hundred, under load). That is
// timeout's own doing, not this package's, and a trap that must finish guards
// itself the same way.
func strayScript(launcher, marker, ready, pidFile string, ignoreTERM bool) string {
	trap := `trap "trap \"\" TERM; echo cleaned > ` + marker + `" EXIT`
	if ignoreTERM {
		trap = `trap "" TERM`
	}
	return launcher + ` bash -c '` + trap + `; echo $$ > ` + pidFile + `; : > ` + ready + `; while true; do sleep 0.05; done' >/dev/null 2>&1
	echo unreachable`
}

// startStray starts strayScript under ApplyGracefulCancel and returns, once
// the work is armed, its cancel, the started command, ApplyGracefulCancel's
// stop and the work's pid.
func startStray(t *testing.T, launcher string, ignoreTERM bool, grace time.Duration) (cancel func(), cmd *exec.Cmd, stop func(), marker string, work int) {
	t.Helper()
	if _, ok := GroupMembers(1); !ok {
		t.Skip("needs a readable process table")
	}
	if _, err := exec.LookPath(strings.Fields(launcher)[0]); err != nil {
		t.Skipf("needs %s on PATH", strings.Fields(launcher)[0])
	}
	dir := t.TempDir()
	marker, ready, pidFile := filepath.Join(dir, "cleanup.txt"), filepath.Join(dir, "ready"), filepath.Join(dir, "pid")
	ctx, cancelCtx := context.WithCancel(context.Background())
	t.Cleanup(cancelCtx)
	cmd = exec.CommandContext(ctx, "bash", "-c", strayScript(launcher, marker, ready, pidFile, ignoreTERM))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 60 * time.Second
	stop = ApplyGracefulCancel(cmd, grace)
	t.Cleanup(stop)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	awaitTrapInstalled(t, ready)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	work, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	// Whatever the outcome, never leave the work running past the test.
	t.Cleanup(func() { _ = syscall.Kill(work, syscall.SIGKILL) })
	if pgid, err := syscall.Getpgid(work); err != nil || pgid == cmd.Process.Pid {
		t.Fatalf("the work's group = %d (err %v), want a group of its own apart from the command's %d", pgid, err, cmd.Process.Pid)
	}
	return cancelCtx, cmd, stop, marker, work
}

// awaitGone waits for pid to exit (or become a zombie awaiting its reaper).
func awaitGone(t *testing.T, pid int, why string) {
	t.Helper()
	giveUp := time.After(conditionTimeout)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for syscall.Kill(pid, 0) == nil && !isZombie(pid) {
		select {
		case <-giveUp:
			t.Fatalf("process %d still running %v after cancel: %s", pid, conditionTimeout, why)
		case <-poll.C:
		}
	}
}

// awaitPIDFile waits for path to hold a pid, and returns it.
func awaitPIDFile(t *testing.T, path, why string) int {
	t.Helper()
	giveUp := time.After(conditionTimeout)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		select {
		case <-giveUp:
			t.Fatalf("no pid in %s within %v: %s", path, conditionTimeout, why)
		case <-poll.C:
		}
	}
}

// TestApplyGracefulCancel_ReachesDescendantsInGroupsOfTheirOwn: a cancelled
// command whose work left its process group — under `timeout`, which takes a
// group of its own, or setsid — must still get that work a SIGTERM, so its
// EXIT trap runs and nothing outlives the command. Before, only the command's
// own group was signalled: its shell died and the work ran on reparented to
// init, trap never run (a cancelled rc-train run's `timeout -k 120 300m bash
// scripts/ci-full-main.sh`, 2026-11-22 and 2026-11-24).
func TestApplyGracefulCancel_ReachesDescendantsInGroupsOfTheirOwn(t *testing.T) {
	for _, launcher := range []string{"timeout -k 120 300", "setsid"} {
		t.Run(strings.Fields(launcher)[0], func(t *testing.T) {
			cancel, cmd, stop, marker, work := startStray(t, launcher, false, DefaultGraceDelay)
			cancel()
			_ = cmd.Wait()
			stop() // what every caller does once Wait has returned

			if got := waitForMarker(t, marker, "cleaned", conditionTimeout); got != "cleaned" {
				t.Fatalf("EXIT trap marker = %q, want %q: the work in a group of its own never got SIGTERM", got, "cleaned")
			}
			awaitGone(t, work, "it outlived the cancelled command")
		})
	}
}

// TestApplyGracefulCancel_KillsDescendantGroupThatIgnoresSIGTERM: grace bounds
// the work in a group of its own too. One that ignores SIGTERM is SIGKILLed
// with its group when grace runs out, though the command's shell, its parent,
// died at once and Wait has long returned.
func TestApplyGracefulCancel_KillsDescendantGroupThatIgnoresSIGTERM(t *testing.T) {
	cancel, cmd, stop, _, work := startStray(t, "setsid", true, 300*time.Millisecond)
	cancel()
	_ = cmd.Wait()
	stop() // what every caller does once Wait has returned
	awaitGone(t, work, "a descendant group that ignored SIGTERM was never killed")
}

// TestApplyGracefulCancel_KillsWhatADescendantGroupForksAfterTheCancel: the
// escalation reaches what a descendant starts after the cancel, however it
// left: here a stray group's leader answers SIGTERM by starting a job in a
// session of its own (setsid, as an embedded postgres does) and exiting at
// once, so the job is reparented to init in a group nothing in custody is in.
// No process table reading proves it the command's; its cgroup does, and the
// escalation kills through it.
func TestApplyGracefulCancel_KillsWhatADescendantGroupForksAfterTheCancel(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("needs setsid on PATH")
	}
	limiter := testLimiter(t)
	dir := t.TempDir()
	ready, childFile := filepath.Join(dir, "ready"), filepath.Join(dir, "child")
	// On SIGTERM the stray leader starts `sleep 1000` with setsid and exits,
	// while its group's members at the cancel (itself and its `sleep 0.05`)
	// are gone within a moment.
	script := `setsid bash -c 'trap "setsid sleep 1000 & echo \$! > ` + childFile + `; exit 0" TERM; : > ` + ready + `; while true; do sleep 0.05; done' >/dev/null 2>&1
	echo unreachable`
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.WaitDelay = 60 * time.Second
	tree := limiter.Prepare(cmd, proctree.Owner{})
	if tree.Cgroup() == "" {
		t.Skip("the test limiter made no cgroup for the command")
	}
	stop := ApplyGracefulCancelIn(cmd, 500*time.Millisecond, tree)
	t.Cleanup(stop)
	if err := tree.Start(ctx, cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(tree.Release)
	awaitTrapInstalled(t, ready)

	cancel()
	_ = cmd.Wait()
	stop() // what every caller does once Wait has returned

	child := awaitPIDFile(t, childFile, "the stray group's leader never started its job on SIGTERM")
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	awaitGone(t, child, "a job started in a session of its own after the cancel outlived the escalation")
	giveUp := time.After(conditionTimeout)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for tree.Populated() {
		select {
		case <-giveUp:
			t.Fatalf("the command's cgroup %s still holds processes %v after the escalation", tree.Cgroup(), conditionTimeout)
		case <-poll.C:
		}
	}
}

// testLimiter is a process limiter over a cgroup subtree of the test's own,
// made in the delegated parent of the test's cgroup (as proctree's tests do),
// so the daemon's own command cgroups are never touched. It skips where the
// test's cgroup is not delegated to this user.
func testLimiter(t *testing.T) *proctree.Limiter {
	t.Helper()
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	own := ""
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			own = p
		}
	}
	parent := filepath.Dir(own)
	if own == "" || own == "/" || parent == "/" {
		t.Skip("not on cgroup v2, or the test's cgroup has no delegated parent")
	}
	parent = filepath.Join("/sys/fs/cgroup", parent)
	// A delegated parent that does not enable pids yet gets it, as
	// proctree's limiter does; a refusal (the parent holds a process) skips.
	ctl, err := os.ReadFile(filepath.Join(parent, "cgroup.subtree_control"))
	if err == nil && !strings.Contains(" "+strings.TrimSpace(string(ctl))+" ", " pids ") {
		err = os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+pids"), 0)
	}
	if err != nil {
		t.Skipf("cgroup %s does not enable the pids controller for its children: %v", parent, err)
	}
	root := filepath.Join(parent, "osutil-test-"+strconv.Itoa(os.Getpid())+"-"+strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Skipf("cgroup %s is not delegated to this user: %v", parent, err)
	}
	t.Cleanup(func() { removeCgroups(t, root) })
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+pids"), 0); err != nil {
		t.Skipf("cannot enable the pids controller under %s: %v", root, err)
	}
	return proctree.New(proctree.Config{CgroupRoot: root})
}

// removeCgroups removes cgroup dir and the cgroups under it, deepest first.
func removeCgroups(t *testing.T, dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			removeCgroups(t, filepath.Join(dir, e.Name()))
		}
	}
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		t.Errorf("removing the test's cgroup %s: %v", dir, err)
	}
}
