// Copyright (c) 2025 Reliant Labs
//go:build linux

package osutil

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

type ioprioCall struct{ which, who, prio int }

func stubIoprioSet(t *testing.T, fn func(which, who, prio int) error) {
	t.Helper()
	orig := ioprioSet
	ioprioSet = fn
	t.Cleanup(func() { ioprioSet = orig })
}

func TestLowerChildIOPriorityInvokesSyscallOnGroup(t *testing.T) {
	var calls []ioprioCall
	stubIoprioSet(t, func(which, who, prio int) error {
		calls = append(calls, ioprioCall{which, who, prio})
		return nil
	})
	if err := LowerChildIOPriority(1234); err != nil {
		t.Fatal(err)
	}
	want := ioprioCall{ioprioWhoPgrp, 1234, ioprioClassBE<<ioprioClassShift | 7}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls = %+v, want [%+v]", calls, want)
	}
}

func TestLowerChildIOPriorityFallsBackToProcess(t *testing.T) {
	var calls []ioprioCall
	stubIoprioSet(t, func(which, who, prio int) error {
		calls = append(calls, ioprioCall{which, who, prio})
		if which == ioprioWhoPgrp {
			return unix.ESRCH
		}
		return nil
	})
	if err := LowerChildIOPriority(1234); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].which != ioprioWhoProcess {
		t.Fatalf("calls = %+v, want pgrp then process", calls)
	}
}

func TestLowerChildIOPriorityReturnsErrorWithoutPanicWhenDenied(t *testing.T) {
	stubIoprioSet(t, func(int, int, int) error { return unix.EPERM })
	err := LowerChildIOPriority(1234)
	if !errors.Is(err, unix.EPERM) {
		t.Fatalf("err = %v, want wrapped EPERM (callers log it at debug and carry on)", err)
	}
}

func TestLowerChildIOPriorityOptOutSkipsSyscall(t *testing.T) {
	keepChildPriorityForTest(t)
	stubIoprioSet(t, func(int, int, int) error {
		t.Fatal("ioprio_set called despite opt-out")
		return nil
	})
	if err := LowerChildIOPriority(1234); err != nil {
		t.Fatal(err)
	}
}

func TestLowerChildIOPriorityInvalidPID(t *testing.T) {
	if err := LowerChildIOPriority(0); err == nil {
		t.Fatal("expected error for pid 0")
	}
}

// Reads the priority back with the real ioprio_get. Skips when the sandbox
// denies ioprio_set outright.
func TestLowerChildIOPriorityAppliedToRealChild(t *testing.T) {
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	if err := LowerChildIOPriority(pid); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("ioprio_set unavailable here: %v", err)
		}
		t.Fatal(err)
	}
	got, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(pid), 0)
	if errno != 0 {
		t.Fatalf("ioprio_get: %v", errno)
	}
	if int(got) != childIOPrio {
		t.Fatalf("ioprio = class %d level %d, want class %d level %d",
			got>>ioprioClassShift, got&0x1fff, ioprioClassBE, ChildIOPrioLevel)
	}
}
