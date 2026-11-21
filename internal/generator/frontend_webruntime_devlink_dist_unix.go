//go:build !windows

package generator

import (
	"os"
	"syscall"
)

// lockFileExclusive takes a blocking flock(2) on f. The kernel drops it when
// the descriptor closes, so a forge killed mid-build never wedges the next.
func lockFileExclusive(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
