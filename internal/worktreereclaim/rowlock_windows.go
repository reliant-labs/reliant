// Copyright (c) 2025 Reliant Labs
//go:build windows

package worktreereclaim

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on path and returns the release func. It
// polls with LOCKFILE_FAIL_IMMEDIATELY until the lock is free or ctx is done.
func lockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	err = pollLock(ctx, func() (bool, error) {
		switch err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err {
		case nil:
			return true, nil
		case windows.ERROR_LOCK_VIOLATION, windows.ERROR_IO_PENDING:
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
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
		f.Close()
	}, nil
}
