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
	cmd := startSleeper(t)
	if err := LowerChildPriority(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	got, err := syscall.Getpriority(syscall.PRIO_PROCESS, cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	// Linux's raw syscall reports 20-nice; darwin reports nice directly.
	if got != ChildNiceValue && got != 20-ChildNiceValue {
		t.Fatalf("priority = %d, want nice %d", got, ChildNiceValue)
	}
}

func TestLowerChildPriorityOptOut(t *testing.T) {
	skipIfAlreadyAtChildNice(t)
	t.Setenv(ChildPriorityEnv, "normal")
	cmd := startSleeper(t)
	if err := LowerChildPriority(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	got, _ := syscall.Getpriority(syscall.PRIO_PROCESS, cmd.Process.Pid)
	if got == ChildNiceValue || got == 20-ChildNiceValue {
		t.Fatalf("priority lowered despite opt-out: %d", got)
	}
}

// skipIfAlreadyAtChildNice skips when this test binary itself already runs at
// ChildNiceValue. The child inherits it, so "opt-out honoured" and "opt-out
// ignored" both leave the child at ChildNiceValue and the test cannot tell
// them apart — it would fail spuriously (an agent under forge runs at nice
// 10), and any rewrite that passed there would pass with the opt-out broken.
// CI runs at nice 0, where the check is meaningful.
func skipIfAlreadyAtChildNice(t *testing.T) {
	t.Helper()
	parent, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatalf("getpriority(self): %v", err)
	}
	if parent == ChildNiceValue || parent == 20-ChildNiceValue {
		t.Skipf("test process already runs at nice %d, so a lowered child is indistinguishable from an inherited one", ChildNiceValue)
	}
}

func TestLowerChildPriorityInvalidPID(t *testing.T) {
	if err := LowerChildPriority(0); err == nil {
		t.Fatal("expected error for pid 0")
	}
}
