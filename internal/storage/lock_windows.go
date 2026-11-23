package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// lockShared blocks until f is locked shared. It only ever waits behind a
// momentary exclusive probe (writerWaiting), never behind a long holder.
func lockShared(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), 0, 0, 1, 0, &windows.Overlapped{})
}

// contended reports whether a failed lock failed because someone else holds it.
func contended(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
