// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"fmt"
	"syscall"
)

// LowerChildPriority lowers the scheduling priority of the just-started child
// with the given PID (see ChildNiceValue). It must be called after the child
// has been started. Raising a nice value is unprivileged.
//
// The child is the leader of its own process group (the spawn sites set
// Setpgid), so the whole group is reniced; this also catches grandchildren
// forked before this call runs. Falls back to the single process when the
// child is not a group leader.
//
// Best-effort by contract: callers should log failures at debug level and
// never fail the spawn because of them.
func LowerChildPriority(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if childPriorityDisabled() {
		return nil
	}
	err := syscall.Setpriority(syscall.PRIO_PGRP, pid, ChildNiceValue)
	if err == nil {
		return nil
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, ChildNiceValue); err != nil {
		return fmt.Errorf("setpriority(%d): %w", pid, err)
	}
	return nil
}
