//go:build !windows

package pgtest

import (
	"errors"
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

// lockFileShared takes a shared flock(2) on f: what a lease holds for as long
// as its process uses the pooled server (lease.go). Any number of holders may
// share it; an exclusive lock on the same file waits for, or with
// tryLockFileExclusive fails on, every one of them. Kernel file locks belong
// to the open file, not to a pid, so they mean the same thing to every
// process sharing the filesystem whatever pid namespace it runs in.
func lockFileShared(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
}

// tryLockFileExclusive reports whether an exclusive lock on f could be taken
// at once, i.e. whether no other open file holds any lock on it. held is true
// when the lock was taken; the caller then releases it by closing f. A lock
// another holder has is (false, nil); any other failure is returned.
func tryLockFileExclusive(f *os.File) (held bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}
