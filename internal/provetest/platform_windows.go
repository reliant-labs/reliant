// Copyright (c) 2025 Reliant Labs
//go:build windows

package provetest

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockExclusive takes a non-blocking exclusive byte-range lock over the
// whole file. Windows releases it when the handle closes, including on
// abnormal termination, so a dead holder never leaves it stuck.
func tryLockExclusive(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0), ^uint32(0),
		&overlapped,
	)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
}

// fileIdent has no cheap equivalent from a FileInfo on Windows; modification
// time and size carry the identity check alone there.
func fileIdent(fs.FileInfo) uint64 { return 0 }
