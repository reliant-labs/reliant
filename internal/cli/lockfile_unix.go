//go:build !windows

package cli

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes an exclusive flock(2) on f without blocking. It reports
// false, with no error, when another open file description holds the lock.
// flock belongs to the open file description, so a second descriptor in the
// SAME process contends like any other process would, and the kernel drops
// the lock when the holder exits.
func tryLockFile(f *os.File) (bool, error) {
	err := flockRetryingEINTR(f, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// lockFile takes an exclusive flock(2) on f, blocking until it is free.
func lockFile(f *os.File) error {
	return flockRetryingEINTR(f, unix.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return flockRetryingEINTR(f, unix.LOCK_UN)
}

// flockRetryingEINTR retries a flock interrupted by a signal. A blocking wait
// can last a whole generate, and the Go runtime signals its own threads.
func flockRetryingEINTR(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
