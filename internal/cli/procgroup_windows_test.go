//go:build windows

package cli

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("self should be alive")
	}
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatal("exited child should not be alive")
	}
	if processAlive(0) || processAlive(-1) {
		t.Fatal("non-positive pid must not be alive")
	}
}

func TestPpidMapContainsSelf(t *testing.T) {
	m := ppidMap()
	if ppid, ok := m[os.Getpid()]; !ok || ppid != os.Getppid() {
		t.Fatalf("ppidMap[self]=%d ok=%v, want %d", ppid, ok, os.Getppid())
	}
}

func TestKillProcessTree(t *testing.T) {
	// cmd -> cmd -> ping gives a child and a grandchild.
	cmd := exec.Command("cmd", "/c", "cmd /c ping -n 30 127.0.0.1 >NUL")
	startInOwnProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	root := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	var desc []int
	waitFor(t, "descendants", func() bool { desc = descendantPIDs(root); return len(desc) >= 1 })
	if err := killProcessTree(root, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM must not error: %v", err)
	}
	if err := killProcessTree(root, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	for _, pid := range append(desc, root) {
		pid := pid
		waitFor(t, "pid "+strconv.Itoa(pid)+" to die", func() bool { return !processAlive(pid) })
	}
}

func TestPortListenerPID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if got := portListenerPID(port); got != os.Getpid() {
		t.Fatalf("portListenerPID(%d)=%d, want %d", port, got, os.Getpid())
	}
}

func TestProcStartTimes(t *testing.T) {
	got := procStartTimes([]int{os.Getpid()})[os.Getpid()]
	if got.IsZero() || got.After(time.Now()) {
		t.Fatalf("bad start time %v", got)
	}
}

func TestProcInspectSelf(t *testing.T) {
	exe, _ := os.Executable()
	if p, ok := procExecPath(os.Getpid()); !ok || !sameFile(p, exe) {
		t.Fatalf("procExecPath=%q ok=%v, want %q", p, ok, exe)
	}
	argv, ok := readProcArgv(os.Getpid())
	if !ok || len(argv) == 0 {
		t.Fatalf("readProcArgv failed: %v %v", argv, ok)
	}
	if _, ok := readProcEnviron(os.Getpid()); !ok {
		t.Fatal("readProcEnviron(self) unreadable")
	}
}

func sameFile(a, b string) bool {
	ai, e1 := os.Stat(a)
	bi, e2 := os.Stat(b)
	return e1 == nil && e2 == nil && os.SameFile(ai, bi)
}

func TestProcessAliveExitCode259(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "259")
	_ = cmd.Run()
	if processAlive(cmd.Process.Pid) {
		t.Fatal("process that exited with code 259 must not read as alive")
	}
}

func TestCtrlBreakSkippedForNonLeader(t *testing.T) {
	// A plain child (no CREATE_NEW_PROCESS_GROUP) is not a group leader.
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	called := false
	sendCtrlBreakWith(cmd.Process.Pid, ctrlBreakFacts, func(uint32) error { called = true; return nil })
	if called {
		t.Fatal("CTRL_BREAK sent to a pid that is not a process-group leader")
	}
}

func TestCtrlBreakFactsLeader(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	startInOwnProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := uint32(cmd.Process.Pid)
	waitFor(t, "group id readable", func() bool { g, ok := readProcGroupID(int(pid)); return ok && g == pid })
}
