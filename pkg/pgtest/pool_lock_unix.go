//go:build !windows

package pgtest

import (
	"os"
	"syscall"
)

// lockFileExclusive takes an exclusive advisory lock on f using flock(2).
//
// flock is tied to the OPEN FILE DESCRIPTION rather than the process, so the
// kernel drops the lock when the descriptor closes — including on abnormal
// termination. That is the property the pool depends on: a crashed
// lock-holder never wedges the pool for everyone else.
//
// The call BLOCKS until the lock is available (no LOCK_NB), which is
// intended — callers want the pool, not a failure, when another process is
// mid-update.
func lockFileExclusive(f *os.File) (unlock func(), err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}, nil
}
