// Copyright (c) 2025 Reliant Labs
//go:build linux

package osutil

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	ioprioClassShift = 13
	ioprioClassBE    = 2
	ioprioWhoProcess = 1
	ioprioWhoPgrp    = 2

	// ChildIOPrioLevel is the best-effort-class level (0 highest, 7 lowest)
	// applied to spawned commands.
	ChildIOPrioLevel = 7
)

// childIOPrio is the packed ioprio value: best-effort class, lowest level.
const childIOPrio = ioprioClassBE<<ioprioClassShift | ChildIOPrioLevel

// ioprioSet is the raw syscall; a var so tests can inject it.
var ioprioSet = func(which, who, prio int) error {
	_, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, uintptr(which), uintptr(who), uintptr(prio))
	if errno != 0 {
		return errno
	}
	return nil
}

// LowerChildIOPriority lowers the I/O scheduling priority of the just-started
// child with the given PID to the best-effort class at its lowest level, the
// I/O counterpart of LowerChildPriority. The child leads its own process group
// (the spawn sites set Setpgid), so the whole group is covered; falls back to
// the single process otherwise. Honors ChildPriorityEnv.
//
// Best-effort by contract: callers should log failures at debug level and
// never fail the spawn because of them (the syscall may be unsupported or
// denied in a sandbox). Only I/O schedulers that implement priorities (BFQ,
// or mq-deadline for ordering) act on it.
func LowerChildIOPriority(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if childPriorityDisabled() {
		return nil
	}
	if err := ioprioSet(ioprioWhoPgrp, pid, childIOPrio); err == nil {
		return nil
	}
	if err := ioprioSet(ioprioWhoProcess, pid, childIOPrio); err != nil {
		return fmt.Errorf("ioprio_set(%d): %w", pid, err)
	}
	return nil
}
