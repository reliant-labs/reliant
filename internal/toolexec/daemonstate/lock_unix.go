// Copyright (c) 2025 Reliant Labs
//go:build !windows

package daemonstate

import (
	"errors"
	"os"
	"syscall"
)

// tryLockExclusive takes a non-blocking exclusive flock. It reports false (with
// no error) when another open file description already holds the lock.
//
// flock is per open-file-description, so a second Acquire inside the SAME
// process is refused too — which is correct: one process must not run two
// daemon runtimes against one data directory either.
func tryLockExclusive(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

func unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
