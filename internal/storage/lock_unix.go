//go:build !windows

package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

// lockShared blocks until f is locked shared. It only ever waits behind a
// momentary exclusive probe (writerWaiting), never behind a long holder.
func lockShared(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_SH) }

// contended reports whether a failed lock failed because someone else holds it.
func contended(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) }
