// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"io/fs"
	"syscall"
	"time"
)

// accessTime is a file's atime, when the platform reports one.
func accessTime(info fs.FileInfo) (time.Time, bool) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Sec, st.Atim.Nsec), true //nolint:unconvert // int32 on some arches
	}
	return time.Time{}, false
}
