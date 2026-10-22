// Copyright (c) 2025 Reliant Labs

//go:build linux || darwin

package memtemp

import (
	"errors"
	"syscall"
)

// tmpfsMagic is TMPFS_MAGIC from linux/magic.h. No other system reports it, so
// usable is false off Linux and Dir falls back to t.TempDir().
const tmpfsMagic = 0x01021994

// usable reports whether dir is a tmpfs with at least minFree available.
func usable(dir string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false
	}
	return int64(st.Type) == tmpfsMagic && st.Bavail*uint64(st.Bsize) >= minFree //nolint:gosec,unconvert // Statfs_t field widths differ by GOARCH
}

// alive reports whether pid names a running process. EPERM means it exists
// and belongs to another user.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
