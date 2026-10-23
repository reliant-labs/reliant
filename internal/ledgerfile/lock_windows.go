//go:build windows

package ledgerfile

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes a blocking exclusive lock on f with LockFileEx, the
// Windows equivalent of flock(2) for this purpose: the lock is held by the
// handle, so closing it — including when the process dies — releases it.
func lockExclusive(f *os.File) (unlock func(), err error) {
	h := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		return nil, err
	}
	return func() {
		var ov windows.Overlapped
		_ = windows.UnlockFileEx(h, 0, 1, 0, &ov)
	}, nil
}
