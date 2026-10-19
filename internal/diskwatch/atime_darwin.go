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
		return time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec), true
	}
	return time.Time{}, false
}
