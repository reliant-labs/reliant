// Copyright (c) 2025 Reliant Labs
//go:build !windows

package candidate

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock takes an exclusive advisory lock on f without waiting. It reports
// false, with no error, when another holder has it.
func tryFlock(f *os.File) (bool, error) {
	switch err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err {
	case nil:
		return true, nil
	case unix.EWOULDBLOCK, unix.EINTR:
		return false, nil
	default:
		return false, err
	}
}

func unflock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
