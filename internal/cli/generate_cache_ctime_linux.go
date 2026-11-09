//go:build linux

package cli

import (
	"io/fs"
	"syscall"
)

// statChangeTime returns the file's inode change time in nanoseconds. Unlike
// the modification time, no ordinary tool can set it: rewriting, replacing or
// `cp -p`-ing a file over another always moves it. That is what lets the
// executable-identity cache trust a stat match (see execIdentity).
func statChangeTime(info fs.FileInfo) int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return st.Ctim.Sec*1e9 + st.Ctim.Nsec
}
