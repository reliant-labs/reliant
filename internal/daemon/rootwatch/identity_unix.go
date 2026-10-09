// Copyright (c) 2025 Reliant Labs

//go:build !windows

package rootwatch

import (
	"os"
	"syscall"
)

func identityOf(info os.FileInfo) (Identity, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, false
	}
	// Dev is int32 on darwin and uint64 on linux; widen both the same way.
	return Identity{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, true //nolint:unconvert // width differs per platform
}
