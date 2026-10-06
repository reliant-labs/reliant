// Copyright (c) 2025 Reliant Labs
//go:build !windows

package provetest

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// tryLockExclusive takes a non-blocking exclusive flock. flock belongs to the
// open file description, so two goroutines of one process that each opened
// the lock file exclude each other exactly as two processes do.
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

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// fileIdent is the file's inode: a rename-over replaces it, so it tells two
// writes of identical bytes apart even within one mtime tick.
func fileIdent(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert // Ino is uint32 on some platforms
	}
	return 0
}
