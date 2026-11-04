// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
		sweep(commands, true)
		if err := os.Remove(commands); err != nil {
			t.Errorf("removing the test's commands cgroup: %v", err)
		}
	})
	return &Limiter{root: root, commands: commands, size: Sizing{CPUs: 1, Budget: shared * 2, Shared: shared, PerCommand: perCommand}}
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
	if note := tree.LimitNote(); !strings.Contains(note, "This command reached its thread limit (30 threads") || strings.Contains(note, "shared") {
		t.Fatalf("LimitNote after the command's own limit refused it: %q", note)
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

// The commands cgroup bounds all commands together: a command well inside its
// own limit is refused at the shared one, is told so, and — bounded by the
// cgroups — keeps the user's RLIMIT_NPROC, which counts every agent's threads.
func TestCommandsCgroupBoundsAllCommandsTogether(t *testing.T) {
	testkit.Slow(t, "starts real process trees in a cgroup")
	l := cgroupLimiter(t, 1000, 40)
	l.nproc = 1
	started, tree := runForker(t, l, 200)
	if started >= 40 {
		t.Fatalf("the forker started %d children under a shared pids.max of 40", started)
	}
	tree.Release()
	if note := tree.LimitNote(); !strings.Contains(note, "shared thread limit (40 threads") || strings.Contains(note, "This command reached") {
		t.Fatalf("LimitNote after the shared limit refused it: %q", note)
	}

	// An RLIMIT_NPROC of 1 would have refused the forker every child.
	if started == 0 {
		t.Fatal("a command with a cgroup had its RLIMIT_NPROC lowered")
	}
	cmd := exec.Command("sleep", "5")
	tree = l.Prepare(cmd, Owner{})
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
	var own unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &own); err != nil {
		t.Fatal(err)
	}
	if got.Cur != own.Cur {
		t.Fatalf("a command with a cgroup has RLIMIT_NPROC %d, want the user's %d", got.Cur, own.Cur)
	}
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
