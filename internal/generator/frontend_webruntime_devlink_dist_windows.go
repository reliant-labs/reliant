//go:build windows

package generator

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes a blocking LockFileEx on f, held by the handle, so
// closing it — including when the process dies — releases it.
func lockFileExclusive(f *os.File) (func(), error) {
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
