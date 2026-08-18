// Copyright (c) 2025 Reliant Labs
//go:build !windows

package instanceid

import (
	"errors"
	"os"
	"syscall"
)

// tryLockExclusiveFile takes a non-blocking exclusive flock. It reports false
// (with no error) when another open file description already holds the lock.
//
// flock is per open-file-description, so two goroutines in the SAME process
// that each opened the file are serialized against each other too — which is
// what makes concurrent in-process first runs converge, not just concurrent
// processes.
func tryLockExclusiveFile(file *os.File) (bool, error) {
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

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
