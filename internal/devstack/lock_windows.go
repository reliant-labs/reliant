//go:build windows

package devstack

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFD takes an exclusive, blocking lock on f with LockFileEx — the Windows
// equivalent of the flock(2) in lock_unix.go. It serializes concurrent
// `forge env up` runs (one per worktree) claiming port blocks in the shared
// registry. The lock belongs to the handle, so it is released when the
// process exits even if unlockFD never runs.
func lockFD(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func unlockFD(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
