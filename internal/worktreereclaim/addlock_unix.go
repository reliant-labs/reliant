// Copyright (c) 2025 Reliant Labs
//go:build !windows

package worktreereclaim

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock takes an advisory lock on f without waiting, exclusive or shared.
// It reports false, with no error, when a conflicting holder has it.
func tryFlock(f *os.File, exclusive bool) (bool, error) {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	switch err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB); err {
	case nil:
		return true, nil
	case unix.EWOULDBLOCK, unix.EINTR:
		return false, nil
	default:
		return false, err
	}
}

func unflock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
