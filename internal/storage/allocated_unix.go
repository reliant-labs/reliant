//go:build !windows

package storage

import (
	"io/fs"
	"syscall"
)

// allocatedBytes is the disk a file occupies: its allocated 512-byte blocks,
// or its length where the platform does not report blocks.
func allocatedBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
