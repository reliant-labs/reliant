// Copyright (c) 2026 Reliant Labs
//go:build !windows

package machinepreflight

import (
	"io/fs"
	"os"
	"syscall"
)

// FileOwner is the uid owning a stat result.
func FileOwner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// ProcessUID is the daemon's uid.
func ProcessUID() (int, bool) { return os.Getuid(), true }

// hasTmpCheck reports whether the platform's /tmp is checked.
const hasTmpCheck = true
