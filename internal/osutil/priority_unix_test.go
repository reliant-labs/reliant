// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"os/exec"
	"syscall"
	"testing"
)

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestLowerChildPriority(t *testing.T) {
	skipIfNicerThanChild(t)
	cmd := startSleeper(t)
	if err := LowerChildPriority(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if got := niceOf(t, cmd.Process.Pid); got != ChildNiceValue {
		t.Fatalf("child nice = %d, want %d", got, ChildNiceValue)
	}
}

// A child that already runs nicer than ChildNiceValue — what every child of a
// daemon started under `nice -n 19` inherits, and what the release lane's
// reliant gate ran at — is left where it is: setting it to ChildNiceValue
// would raise its priority, which needs CAP_SYS_NICE, and LowerChildPriority
// only ever lowers. Renicing the child up to 19 first is unprivileged, so this
// creates the condition on any host.
func TestLowerChildPriorityLeavesNicerChild(t *testing.T) {
	cmd := startSleeper(t)
	const nicer = 19
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, nicer); err != nil {
		t.Fatalf("setpriority(child, %d): %v", nicer, err)
	}
	if err := LowerChildPriority(cmd.Process.Pid); err != nil {
		t.Fatalf("LowerChildPriority on a child already at nice %d: %v", nicer, err)
	}
	if got := niceOf(t, cmd.Process.Pid); got != nicer {
		t.Fatalf("child nice = %d, want it left at %d", got, nicer)
	}
}

func TestLowerChildPriorityOptOut(t *testing.T) {
	skipIfAlreadyAtChildNice(t)
	keepChildPriorityForTest(t)
	cmd := startSleeper(t)
	if err := LowerChildPriority(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if got := niceOf(t, cmd.Process.Pid); got == ChildNiceValue {
		t.Fatalf("priority lowered despite opt-out: nice %d", got)
	}
}

// skipIfAlreadyAtChildNice skips when this test binary itself already runs at
// ChildNiceValue or nicer. The child inherits it, so "opt-out honoured" and
// "opt-out ignored" both leave the child where the parent is and the test
// cannot tell them apart — it would fail spuriously (an agent under forge runs
// at nice 10), and any rewrite that passed there would pass with the opt-out
// broken. CI runs at nice 0, where the check is meaningful.
func skipIfAlreadyAtChildNice(t *testing.T) {
	t.Helper()
	if parent := niceOf(t, 0); parent >= ChildNiceValue {
		t.Skipf("test process already runs at nice %d (>= %d), so a lowered child is indistinguishable from an inherited one", parent, ChildNiceValue)
	}
}

// skipIfNicerThanChild skips when this test binary runs nicer than
// ChildNiceValue: its child inherits that niceness, and bringing it back to
// ChildNiceValue is a priority raise that needs CAP_SYS_NICE, which
// LowerChildPriority rightly never attempts. TestLowerChildPriorityLeavesNicerChild
// covers that case on every host.
func skipIfNicerThanChild(t *testing.T) {
	t.Helper()
	if parent := niceOf(t, 0); parent > ChildNiceValue {
		t.Skipf("test process runs at nice %d, nicer than %d: a child at nice %d needs CAP_SYS_NICE to create", parent, ChildNiceValue, ChildNiceValue)
	}
}

// niceOf returns the nice value of pid (0: this process).
func niceOf(t *testing.T, pid int) int {
	t.Helper()
	nice, err := getNice(syscall.PRIO_PROCESS, pid)
	if err != nil {
		t.Fatalf("getpriority(%d): %v", pid, err)
	}
	return nice
}

func TestLowerChildPriorityInvalidPID(t *testing.T) {
	if err := LowerChildPriority(0); err == nil {
		t.Fatal("expected error for pid 0")
	}
}
