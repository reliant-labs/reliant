// Copyright (c) 2025 Reliant Labs
//go:build windows

package candidate

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryFlock takes an exclusive lock on f's first byte without waiting. It
// reports false, with no error, when another holder has it.
func tryFlock(f *os.File) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY | windows.LOCKFILE_EXCLUSIVE_LOCK)
	switch err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped)); err {
	case nil:
		return true, nil
	case windows.ERROR_LOCK_VIOLATION, windows.ERROR_IO_PENDING:
		return false, nil
	default:
		return false, err
	}
}

func unflock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
