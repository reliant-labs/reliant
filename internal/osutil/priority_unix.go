// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"errors"
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
// Setpgid), and grandchildren forked before this call runs are in that group
// too. The check is made per process, never once for the group: one
// setpriority(PRIO_PGRP) would set every member to ChildNiceValue, raising a
// member that already runs nicer. So the leader is lowered on its own, then
// each other live member the process table lists (GroupMembers; Linux). Where
// the table cannot be read only the leader is lowered; the grandchildren it
// forks after this call inherit its niceness anyway.
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
	if err := lowerPriority(pid); err != nil {
		return fmt.Errorf("setpriority(%d): %w", pid, err)
	}
	members, _ := GroupMembers(pid)
	var errs []error
	for _, m := range members {
		if m.PID == pid {
			continue
		}
		// A member that exited since it was listed has nothing to lower.
		if err := lowerPriority(m.PID); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("setpriority(%d, group %d): %w", m.PID, pid, err))
		}
	}
	return errors.Join(errs...)
}

// lowerPriority sets process pid to ChildNiceValue unless it already runs at
// that niceness or nicer.
func lowerPriority(pid int) error {
	nice, err := getNice(syscall.PRIO_PROCESS, pid)
	if err != nil {
		return err
	}
	if nice >= ChildNiceValue {
		return nil
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, ChildNiceValue)
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
