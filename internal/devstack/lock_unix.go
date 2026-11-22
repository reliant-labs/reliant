//go:build !windows

package devstack

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFD takes an exclusive, blocking advisory lock on f (flock(2)).
// It blocks until the lock is available; the OS releases it when the
// process exits even if unlockFD never runs.
func lockFD(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockFD(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
