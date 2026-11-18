//go:build windows

package hostslot

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows counterpart of lock_unix.go's flock: LockFileEx on one byte of
// the file. The lock belongs to the handle, so a second handle in the SAME
// process contends like another process would, and Windows releases it when
// the holder exits.
//
// The locked byte sits far past the end of the file rather than at offset 0:
// Windows byte-range locks are mandatory, so locking the byte the holder
// record starts at would make that record unreadable to the waiter it is
// written for. Locking beyond EOF is explicitly allowed.
const (
	lockByteOffsetHigh = 0x7fffffff
	lockByteLength     = 1
)

func tryLock(f *os.File) (bool, error) {
	ol := &windows.Overlapped{OffsetHigh: lockByteOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockByteLength, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockByteOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockByteLength, 0, ol)
}
