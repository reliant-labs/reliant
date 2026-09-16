// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"errors"
	"os"
	"syscall"
)

// terminateGracefully sends SIGTERM to the child's process GROUP, falling back
// to the child alone when it has no group of its own.
//
// The group is the point: `bash -c` forks for anything beyond a single simple
// command, so the process actually doing the work — and holding git's
// index.lock — is usually a grandchild that a signal to the direct child
// never reaches.
//
// A negative pid addresses the group. That is safe here specifically because
// Cancel runs before Wait reaps the child: the pid is still un-reaped, so it
// cannot have been recycled onto an unrelated process.
//
// ESRCH means the process (or the whole group) is already gone; that is
// reported as os.ErrProcessDone so os/exec leaves a successful Wait alone
// rather than inventing a cancellation failure.
func terminateGracefully(proc *os.Process) error {
	pid := proc.Pid
	if pid <= 0 {
		return os.ErrProcessDone
	}

	err := syscall.Kill(-pid, syscall.SIGTERM)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return os.ErrProcessDone
	case errors.Is(err, syscall.EPERM):
		// Some group member is not ours to signal. Others may have received
		// it; fall through to the direct child so the common case still gets
		// its handler.
	}

	// No process group of its own (caller forgot Setpgid), or a partial
	// failure above: signal the child directly. Signal() reports
	// os.ErrProcessDone itself when the child is already reaped.
	if sigErr := proc.Signal(syscall.SIGTERM); sigErr != nil {
		return sigErr
	}
	return nil
}
