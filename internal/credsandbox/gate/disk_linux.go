// Copyright (c) 2025 Reliant Labs

//go:build linux

package gate

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockSlot takes path's lock without waiting: errSlotBusy when another
// process holds it. The OS drops the lock when its holder exits.
func lockSlot(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errSlotBusy
		}
		return nil, err
	}
	return f, nil
}

// slotSize is the size of the filesystem mounted at slot, which must be one
// of its own: a directory on its parent's filesystem bounds nothing.
func slotSize(parent, slot string) (int64, error) {
	var ps, ss unix.Stat_t
	if err := unix.Stat(parent, &ps); err != nil {
		return 0, err
	}
	if err := unix.Stat(slot, &ss); err != nil {
		return 0, err
	}
	if ps.Dev == ss.Dev {
		return 0, fmt.Errorf("disk slot %s is not a filesystem of its own (mount a size-limited filesystem there)", slot)
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(slot, &fs); err != nil {
		return 0, err
	}
	return int64(fs.Blocks) * int64(fs.Bsize), nil
}
