// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// helperEnv makes the test binary a forker: it starts PROCTREE_FORKS
// children (each `sleep 3`) and prints how many it could start. Go's fork
// fails at once on EAGAIN, where bash retries with a backoff of seconds.
const helperEnv = "PROCTREE_TEST_FORKER"

func TestMain(m *testing.M) {
	if n, err := strconv.Atoi(os.Getenv(helperEnv)); err == nil {
		started := 0
		var kids []*exec.Cmd
		for range n {
			c := exec.Command("sleep", "3")
			if c.Start() != nil {
				continue
			}
			started++
			kids = append(kids, c)
		}
		fmt.Println("started", started)
		for _, c := range kids {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runForker runs the forker as a tree of l and returns how many children it
// could start.
func runForker(t *testing.T, l *Limiter, forks int) (int, *Tree) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	// One P keeps the forker's own threads few: they count against the limit too.
	cmd.Env = append(os.Environ(), helperEnv+"="+strconv.Itoa(forks), "GOMAXPROCS=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	tree := l.Prepare(cmd, Owner{})
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("forker: %v\n%s", err, out.String())
	}
	f := strings.Fields(out.String())
	if len(f) != 2 {
		t.Fatalf("forker printed %q", out.String())
	}
	n, _ := strconv.Atoi(f[1])
	return n, tree
}

// cgroupLimiter is a limiter over a commands cgroup of the test's own in the
// delegated cgroup, whose limits are perCommand and shared threads: the
// daemon's own commands cgroup is never touched. It skips where the test's
// cgroup is not delegated to this user.
func cgroupLimiter(t *testing.T, perCommand, shared int) *Limiter {
	t.Helper()
	root, why := delegatedRoot()
	if root == "" {
		t.Skip("no delegated cgroup here: " + why)
	}
	commands, err := makeCommandsCgroup(root, fmt.Sprintf("proctree-test-%d-%s", os.Getpid(), t.Name()), shared)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		(&Limiter{commands: commands}).sweepOwn()
		if err := os.Remove(commands); err != nil {
			t.Errorf("removing the test's commands cgroup: %v", err)
		}
	})
	return &Limiter{root: root, commands: commands, size: Sizing{CPUs: 1, Budget: shared * 2, Shared: shared, PerCommand: perCommand}}
}

// A cgroup held for a prepared command survives another Prepare's sweep of
// this process's empty command cgroups, and the sweep after its release takes
// it. Plain directories stand in for cgroups: sweep treats one with no
// cgroup.events as empty.
func TestSweepSparesTheCgroupsOfUnreleasedTrees(t *testing.T) {
	dir := t.TempDir()
	l := &Limiter{commands: dir}
	own := func(seq int) string { return filepath.Join(dir, fmt.Sprintf("%s%d-%d", cmdPrefix, os.Getpid(), seq)) }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if err := l.hold(own(1)); err != nil {
		t.Fatal(err)
	}
	// A leftover of a released tree whose processes have since exited.
	if err := os.Mkdir(own(2), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.hold(own(3)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.drop(own(3)) })
	if !exists(own(1)) {
		t.Fatalf("a second Prepare's sweep removed the held cgroup %s: its command's start would fail ENOENT", own(1))
	}
	if exists(own(2)) {
		t.Fatalf("the sweep left the released, empty cgroup %s", own(2))
	}

	l.drop(own(1))
	if exists(own(1)) {
		t.Fatalf("drop left the empty cgroup %s", own(1))
	}
	l.sweepOwn()
	if !exists(own(3)) {
		t.Fatalf("a sweep removed the still-held cgroup %s", own(3))
	}
}

// A command cgroup a killed slot wrapper left its two cgroups in
// (internal/heavyjob) is still removed, by drop and by a sweep: a cgroup with
// children cannot be removed until they are. Plain directories stand in for
// cgroups.
func TestDropAndSweepRemoveTheCgroupsLeftInACommandCgroup(t *testing.T) {
	dir := t.TempDir()
	l := &Limiter{commands: dir}
	own := func(seq int) string { return filepath.Join(dir, fmt.Sprintf("%s%d-%d", cmdPrefix, os.Getpid(), seq)) }
	leave := func(cg string) {
		for _, child := range []string{"heavy-9-caller", "heavy-9-command"} {
			if err := os.Mkdir(filepath.Join(cg, child), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.hold(own(1)); err != nil {
		t.Fatal(err)
	}
	leave(own(1))
	l.drop(own(1))
	if _, err := os.Stat(own(1)); err == nil {
		t.Fatalf("drop left %s, whose wrapper's cgroups are empty", own(1))
	}

	// Released while a leftover process kept it, emptied since.
	if err := os.Mkdir(own(2), 0o755); err != nil {
		t.Fatal(err)
	}
	leave(own(2))
	l.sweepOwn()
	if _, err := os.Stat(own(2)); err == nil {
		t.Fatalf("the sweep left %s, whose wrapper's cgroups are empty", own(2))
	}
}

// A limiter built while another of this process holds a cgroup for a command
// not yet started (a daemon runtime that starts again) leaves that cgroup
// alone: New sweeps only the leftovers of daemons that have exited, no
// limiter's sweep takes another's holds, and the two never name a cgroup
// alike. New used to remove it, and the first limiter's command then failed
// "fork/exec …: no such file or directory" (reliant #2826). Plain directories
// stand in for cgroups: sweep treats one with no cgroup.events as empty.
func TestANewLimiterSparesTheCgroupsThisProcessHolds(t *testing.T) {
	root := t.TempDir()
	commands := filepath.Join(root, commandsName)
	if err := os.Mkdir(commands, 0o755); err != nil {
		t.Fatal(err)
	}
	// Writable twice over, as a cgroup's is: New writes it both for pids
	// and for cpu.
	if err := os.WriteFile(filepath.Join(commands, "cgroup.subtree_control"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	name := func(pid, seq int) string { return fmt.Sprintf("%s%d-%d", cmdPrefix, pid, seq) }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	first := &Limiter{root: root, commands: commands}
	// Sequence 0, which Prepare never names.
	prepared := filepath.Join(commands, name(os.Getpid(), 0))
	if err := first.hold(prepared); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.drop(prepared) })
	// An exited daemon's leftovers, beside the commands cgroup and in it: a
	// pid above pid_max belongs to no process.
	data, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Skip("no /proc/sys/kernel/pid_max: ", err)
	}
	pidMax, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	leftovers := []string{filepath.Join(root, name(pidMax+1, 1)), filepath.Join(commands, name(pidMax+1, 2))}
	for _, p := range leftovers {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	second := New(Config{CgroupRoot: root})
	if second.commands != commands {
		t.Fatalf("the second limiter's commands cgroup is %q, want %q (%s)", second.commands, commands, second.why)
	}
	if !exists(prepared) {
		t.Fatalf("New removed %s, which another limiter of this process holds for a command not yet started: its start would fail ENOENT", prepared)
	}
	for _, p := range leftovers {
		if exists(p) {
			t.Fatalf("New left %s, an exited daemon's empty cgroup", p)
		}
	}
	second.sweepOwn()
	if !exists(prepared) {
		t.Fatalf("the second limiter's sweep removed %s, which the first holds", prepared)
	}

	a := first.Prepare(exec.Command("true"), Owner{})
	b := second.Prepare(exec.Command("true"), Owner{})
	t.Cleanup(func() { a.Started(0); b.Started(0) })
	if a.Cgroup() == "" || b.Cgroup() == "" || a.Cgroup() == b.Cgroup() {
		t.Fatalf("the two limiters prepared commands in cgroups %q and %q: each needs one of its own", a.Cgroup(), b.Cgroup())
	}
}

// Two commands prepared before either starts both start in their own
// cgroups. The second Prepare used to sweep the first's empty cgroup away, so
// the first failed "fork/exec …: no such file or directory" (reliant #2416).
func TestCommandsPreparedTogetherBothStart(t *testing.T) {
	testkit.Slow(t, "starts real processes in a cgroup")
	l := cgroupLimiter(t, 30, 1000)
	first, second := exec.Command("true"), exec.Command("true")
	firstTree := l.Prepare(first, Owner{})
	secondTree := l.Prepare(second, Owner{})
	for _, c := range []struct {
		cmd  *exec.Cmd
		tree *Tree
	}{{first, firstTree}, {second, secondTree}} {
		if c.tree.Cgroup() == "" {
			t.Fatal("a prepared command has no cgroup")
		}
		if err := c.cmd.Start(); err != nil {
			c.tree.Started(0)
			t.Fatalf("starting a command in %s: %v", c.tree.Cgroup(), err)
		}
		c.tree.Started(c.cmd.Process.Pid)
		_ = c.cmd.Wait()
		c.tree.Release()
	}
}

// A command whose cgroup was removed after Prepare made it starts anyway, in
// a new cgroup, instead of failing "fork/exec …: no such file or directory"
// (reliant #2826). A missing program still fails, with its own error, whether
// or not the cgroup is there. Runs only where the daemon's cgroup is
// delegated to this user.
func TestStartRecreatesACgroupRemovedBeforeItsCommandStarted(t *testing.T) {
	testkit.Slow(t, "starts real processes in a cgroup")
	l := cgroupLimiter(t, 64, 1000)

	cmd := exec.Command("cat", "/proc/self/cgroup")
	var out bytes.Buffer
	cmd.Stdout = &out
	tree := l.Prepare(cmd, Owner{})
	gone := tree.Cgroup()
	if gone == "" {
		t.Fatal("a prepared command has no cgroup")
	}
	// What a sweep that took a held cgroup did.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if err := tree.Start(context.Background(), cmd); err != nil {
		t.Fatalf("starting a command whose cgroup %s was removed: %v", gone, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	tree.Release()
	if now := tree.Cgroup(); now == "" || now == gone || !strings.Contains(out.String(), strings.TrimPrefix(now, cgroupFS)+"\n") {
		t.Fatalf("the command ran in cgroup %q (/proc/self/cgroup %q), want a new one in place of %s", now, out.String(), gone)
	}

	for _, remove := range []bool{false, true} {
		missing := exec.Command(filepath.Join(t.TempDir(), "no-such-program"))
		tree := l.Prepare(missing, Owner{})
		if remove {
			if err := os.Remove(tree.Cgroup()); err != nil {
				t.Fatal(err)
			}
		}
		err := tree.Start(context.Background(), missing)
		if !errors.Is(err, syscall.ENOENT) || !strings.Contains(err.Error(), "no-such-program") {
			t.Fatalf("starting a missing program (cgroup removed: %v): %v, want its ENOENT", remove, err)
		}
		if _, err := os.Stat(tree.Cgroup()); tree.Cgroup() != "" && !os.IsNotExist(err) {
			t.Fatalf("a command that did not start left its cgroup %s (%v)", tree.Cgroup(), err)
		}
	}
}

// Only a start refused because the tree's cgroup is gone is retried: a
// missing program or working directory is ENOENT too, with the cgroup still
// in place.
func TestCgroupVanishedOnlyWhenTheCgroupIsGone(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, cmdPrefix+"1-1")
	if err := os.Mkdir(present, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, cmdPrefix+"1-2")
	startErr := func(errno syscall.Errno) error {
		return &os.PathError{Op: "fork/exec", Path: "/usr/bin/bash", Err: errno}
	}
	for _, c := range []struct {
		name string
		tree *Tree
		err  error
		want bool
	}{
		{"removed cgroup", &Tree{dir: gone}, startErr(syscall.ENOENT), true},
		{"dying cgroup", &Tree{dir: gone}, startErr(syscall.ENODEV), true},
		{"missing program or working directory", &Tree{dir: present}, startErr(syscall.ENOENT), false},
		{"another error", &Tree{dir: gone}, startErr(syscall.EACCES), false},
		{"no cgroup", &Tree{}, startErr(syscall.ENOENT), false},
		{"no tree", nil, startErr(syscall.ENOENT), false},
	} {
		if got := c.tree.cgroupVanished(c.err); got != c.want {
			t.Errorf("%s: cgroupVanished = %v, want %v", c.name, got, c.want)
		}
	}
}

// renew makes a Cmd whose Start failed startable again, with every field the
// caller set and its context: cancelling the context kills what the renewed
// Cmd started, through the caller's pointer.
func TestRenewMakesAFailedCmdStartable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `echo "$PROCTREE_RENEW" "$(pwd)"; exec sleep 300`)
	cmd.Env = []string{"PROCTREE_RENEW=kept"}
	cmd.Dir = filepath.Join(t.TempDir(), "missing")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd.Stdout = w
	if err := cmd.Start(); err == nil {
		t.Fatal("a command in a missing directory started")
	}
	if err := cmd.Start(); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("os/exec took a second Start of one Cmd (%v): renew is not needed", err)
	}
	cmd.Dir = t.TempDir()
	renew(ctx, cmd)
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		t.Fatalf("starting the renewed command: %v", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if want := "kept " + cmd.Dir + "\n"; line != want {
		t.Fatalf("the renewed command printed %q (%v), want %q", line, err, want)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelling the context did not kill the renewed command")
	}
}

// renew keeps the program cmd names exactly: a relative Path, which execve
// resolves in cmd.Dir, is not looked up again on $PATH or in the daemon's
// directory, where it would name another program or none.
func TestRenewKeepsARelativeProgramInTheCommandsDir(t *testing.T) {
	dir := t.TempDir()
	const name = "proctree-renew-tool"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho ran in \"$(pwd)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "/bin/true")
	cmd.Path, cmd.Args, cmd.Dir = name, []string{name}, dir
	renew(context.Background(), cmd)
	if cmd.Path != name || cmd.Err != nil || len(cmd.Args) != 1 || cmd.Args[0] != name {
		t.Fatalf("renew changed the program: Path %q, Args %q, Err %v; want Path %q", cmd.Path, cmd.Args, cmd.Err, name)
	}
	out, err := cmd.Output()
	if want := "ran in " + dir + "\n"; err != nil || string(out) != want {
		t.Fatalf("the renewed command printed %q (%v), want %q", out, err, want)
	}
}

// A delegated cgroup (the cloud entrypoint's /daemon) bounds each command to
// its own pids.max, says so in LimitNote, kills it whole with cgroup.kill,
// and is removed once empty. Runs only where the daemon's cgroup is
// delegated to this user.
func TestCommandCgroupBoundsAndKillsTheTree(t *testing.T) {
	testkit.Slow(t, "starts real process trees in a cgroup")
	l := cgroupLimiter(t, 30, 1000)
	started, tree := runForker(t, l, 200)
	if started >= 30 {
		t.Fatalf("the forker started %d children under pids.max 30", started)
	}
	// Before Linux 6.13 there is no pids.events.local, so refusals cannot tell the
	// command's own limit from the shared one, and LimitNote names both (refused.either).
	if _, err := os.Stat(filepath.Join(tree.Cgroup(), "pids.events.local")); err == nil {
		if note := tree.LimitNote(); !strings.Contains(note, "This command reached its thread limit (30 threads") || strings.Contains(note, "shared") {
			t.Fatalf("LimitNote after the command's own limit refused it: %q", note)
		}
	} else if note := tree.LimitNote(); !strings.Contains(note, "The kernel refused this command") || !strings.Contains(note, "its own (30 threads at once") {
		t.Fatalf("LimitNote after a thread limit refused it, on a kernel without pids.events.local: %q", note)
	}
	tree.Release()
	if _, err := os.Stat(tree.Cgroup()); !os.IsNotExist(err) {
		t.Fatalf("the command cgroup %s outlived its empty tree (%v)", tree.Cgroup(), err)
	}
	if note := tree.LimitNote(); !strings.Contains(note, "(30 threads") {
		t.Fatalf("LimitNote after Release lost what it read: %q", note)
	}

	// A member that left the process group (setsid) is out of a group kill's
	// reach, not the cgroup's.
	cmd := exec.Command("setsid", "sleep", "300")
	tree = l.Prepare(cmd, Owner{})
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Skip("no setsid: ", err)
	}
	tree.Started(cmd.Process.Pid)
	if !tree.Populated() || !tree.Kill() {
		t.Fatal("the tree is not populated, or cgroup.kill failed")
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for tree.Populated() {
		if time.Now().After(deadline) {
			t.Fatal("cgroup.kill left the tree populated")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tree.Release()
}

// Without the cgroups, a command's RLIMIT_NPROC is the reserve.
func TestNoCgroupLowersRlimit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is exempt from RLIMIT_NPROC")
	}
	l := New(Config{CgroupRoot: "-"})
	if l.nproc == 0 {
		t.Skip("RLIMIT_NPROC is unlimited here")
	}
	cmd := exec.Command("sleep", "5")
	tree := l.Prepare(cmd, Owner{})
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); tree.Release() })
	var got unix.Rlimit
	if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_NPROC, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cur != l.nproc {
		t.Fatalf("RLIMIT_NPROC %d, want %d", got.Cur, l.nproc)
	}
	if tree.LimitNote() != "" {
		t.Fatal("a tree with no cgroup has nothing to report")
	}
}

// Prepare always gives the command its own process group, and keeps what a
// caller already set (credsandbox's namespaces), even with no limiter.
func TestPrepareKeepsTheCallersAttributes(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	var l *Limiter
	tree := l.Prepare(cmd, Owner{})
	if !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Cloneflags != syscall.CLONE_NEWUSER {
		t.Fatalf("SysProcAttr = %+v", cmd.SysProcAttr)
	}
	if tree.Cgroup() != "" || tree.Populated() || tree.Kill() {
		t.Fatal("a nil limiter's tree has a cgroup")
	}
	tree.Started(0)
	tree.Release()
}
