// Copyright (c) 2025 Reliant Labs
//go:build !windows

package worktreereclaim

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock on path and returns the release
// func. It polls with LOCK_NB until the lock is free or ctx is done, so a row
// whose lock another process holds cannot outlast the caller's deadline. The
// lock dies with the process, so a crash never wedges it.
func lockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	err = pollLock(ctx, func() (bool, error) {
		switch err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err {
		case nil:
			return true, nil
		case unix.EWOULDBLOCK, unix.EINTR:
			return false, nil
		default:
			return false, err
		}
	})
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}
