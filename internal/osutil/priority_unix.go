// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"fmt"
	"runtime"
	"syscall"
)

// LowerChildPriority lowers the scheduling priority of the just-started child
// with the given PID to ChildNiceValue. It must be called after the child has
// been started. Raising a nice value is unprivileged.
//
// It only ever lowers. A child that already runs at ChildNiceValue or nicer —
// inherited from a parent that was itself niced past it, as a daemon started
// under `nice -n 19` or a release lane that renices itself is — is left alone
// and the call succeeds: setting it back to ChildNiceValue would RAISE its
// priority, which an unprivileged process may not do (EACCES) and a privileged
// one should not.
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
	if lowerPriority(syscall.PRIO_PGRP, pid) == nil {
		return nil
	}
	if err := lowerPriority(syscall.PRIO_PROCESS, pid); err != nil {
		return fmt.Errorf("setpriority(%d): %w", pid, err)
	}
	return nil
}

// lowerPriority sets who to ChildNiceValue unless it already runs at that
// niceness or nicer. For PRIO_PGRP the kernel reports the group's least nice
// member, so the group is left alone only when every member already is.
func lowerPriority(which, who int) error {
	nice, err := getNice(which, who)
	if err != nil {
		return err
	}
	if nice >= ChildNiceValue {
		return nil
	}
	return syscall.Setpriority(which, who, ChildNiceValue)
}

// getNice returns the nice value of who. Linux's raw getpriority syscall
// reports 20-nice (so that it never returns a negative number); the BSDs and
// darwin report the nice value itself.
func getNice(which, who int) (int, error) {
	prio, err := syscall.Getpriority(which, who)
	if err != nil {
		return 0, err
	}
	if runtime.GOOS == "linux" {
		return 20 - prio, nil
	}
	return prio, nil
}
