//go:build windows

package cli

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows counterpart of lockfile_unix.go's flock: LockFileEx on one byte
// of the lock file. The lock belongs to the file handle, so a second handle in
// the SAME process contends like another process would, and Windows releases
// it when the holder exits.
//
// The locked byte sits far past the end of the file rather than at offset 0.
// Windows byte-range locks are mandatory, so locking the byte the holder
// record starts at would make that record unreadable to the very waiter it is
// written for. Locking beyond EOF is explicitly allowed.
const (
	lockByteOffsetHigh = 0x7fffffff
	lockByteLength     = 1
)

func tryLockFile(f *os.File) (bool, error) {
	err := lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return err == nil, err
}

func lockFile(f *os.File) error {
	return lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockByteOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockByteLength, 0, ol)
}

func lockFileEx(f *os.File, flags uint32) error {
	ol := &windows.Overlapped{OffsetHigh: lockByteOffsetHigh}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, lockByteLength, 0, ol)
}
