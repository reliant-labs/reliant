//go:build unix

package toolfetch

import (
	"io/fs"
	"syscall"
)

// fileOwner returns the owning uid and gid of info.
func fileOwner(info fs.FileInfo) (uid, gid uint32, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return st.Uid, st.Gid, true
}
