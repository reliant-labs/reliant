//go:build windows

package pgtest

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes an exclusive lock on f using LockFileEx.
//
// This is the Windows counterpart to flock(2) and is chosen for the same
// property the pool relies on: the lock is held by the FILE HANDLE, so
// Windows releases it when the handle closes — including when the process
// dies abnormally. A crashed lock-holder therefore cannot wedge the pool.
//
// LOCKFILE_EXCLUSIVE_LOCK without LOCKFILE_FAIL_IMMEDIATELY blocks until the
// lock is available, matching the unix path: callers want to wait for the
// pool, not fail because another process is mid-update.
//
// The byte range is the whole file (0xFFFFFFFF/0xFFFFFFFF), which is the
// conventional way to express a whole-file lock on Windows — the lockfile
// carries no data, it exists only to be locked.
func lockFileExclusive(f *os.File) (unlock func(), err error) {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	); err != nil {
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 0xFFFFFFFF, 0xFFFFFFFF, ol)
	}, nil
}
