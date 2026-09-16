// Copyright (c) 2025 Reliant Labs
//go:build !windows

package gitutil

import (
	"os"
	"syscall"
)

// fileIdentity returns a value that changes when the file at path is replaced
// by a different file, even at the same path.
//
// Device plus inode, because the whole point is to distinguish "the same lock
// I judged a moment ago" from "a new lock a new git process just created".
// Path identity cannot do that — git's normal cycle removes index.lock and a
// concurrent process may create a fresh one at the same name microseconds
// later, and unlinking that one would be removing a live lock.
func fileIdentity(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// Mix device and inode: inode numbers are only unique within a device.
	return uint64(st.Dev)<<32 ^ uint64(st.Ino), true
}
