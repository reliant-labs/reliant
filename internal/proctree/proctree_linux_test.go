// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

// userTasks counts this user's tasks (threads included), which is what
// RLIMIT_NPROC is checked against.
func userTasks(t *testing.T) uint64 {
	t.Helper()
	uid := strconv.Itoa(os.Getuid())
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var n uint64
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		mine, threads := false, uint64(0)
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "Uid:"); ok {
				mine = len(strings.Fields(v)) > 0 && strings.Fields(v)[0] == uid
			}
			if v, ok := strings.CutPrefix(line, "Threads:"); ok {
				threads, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			}
		}
		if mine {
			n += threads
		}
	}
	return n
}

// The RLIMIT_NPROC reserve: a command whose share of the user's process limit
// is used up cannot fork, while this process (the daemon's stand-in) is not
// limited at all. The share is set just above what the user runs now, so the
// forker gets a few children and is refused the rest at once.
func TestCommandForksStopAtTheNprocReserve(t *testing.T) {
	testkit.Slow(t, "starts hundreds of real processes and scans /proc")
	if os.Geteuid() == 0 {
		t.Skip("root is exempt from RLIMIT_NPROC")
	}
	const headroom, forks = 60, 400
	l := &Limiter{nproc: userTasks(t) + headroom}
	started, _ := runForker(t, l, forks)
	// The forker's own threads count too, and other processes of this user
	// come and go while it runs: judge "most were refused", not an exact
	// number.
	if started >= forks/2 {
		t.Fatalf("the forker started %d of %d children under a reserve of ~%d; RLIMIT_NPROC was not applied", started, forks, headroom)
	}

	// The same forker unlimited starts them all: the bound came from the
	// limiter, not from this machine.
	if started, _ := runForker(t, &Limiter{}, forks); started != forks {
		t.Fatalf("unlimited forker started %d of %d", started, forks)
	}
}

// A delegated cgroup (the cloud entrypoint's /daemon) bounds each command to
// its own pids.max, kills it whole with cgroup.kill, and is removed once
// empty. Runs only where the daemon's cgroup is delegated to this user.
func TestCommandCgroupBoundsAndKillsTheTree(t *testing.T) {
	testkit.Slow(t, "starts real process trees in a cgroup")
	root, why := delegatedRoot()
	if root == "" {
		t.Skip("no delegated cgroup here: " + why)
	}
	l := &Limiter{root: root, pidsMax: 30}
	started, tree := runForker(t, l, 200)
	if started >= 30 {
		t.Fatalf("the forker started %d children under pids.max 30", started)
	}
	tree.Release()
	if _, err := os.Stat(tree.Cgroup()); !os.IsNotExist(err) {
		t.Fatalf("the command cgroup %s outlived its empty tree (%v)", tree.Cgroup(), err)
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
