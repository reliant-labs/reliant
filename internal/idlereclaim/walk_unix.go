// Copyright (c) 2026 Reliant Labs

//go:build unix

package idlereclaim

import (
	"os"
	"syscall"
)

// deviceOf is the filesystem info's entry is on.
func deviceOf(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev is int32 on some unix platforms
}
