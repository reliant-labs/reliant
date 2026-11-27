// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"syscall"

	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// statVolume is one statfs reading of the volume holding p.
func statVolume(p string) (diskwatch.Usage, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return diskwatch.Usage{}, false
	}
	bs := uint64(st.Bsize) //nolint:gosec // a block size is positive
	return diskwatch.Usage{
		TotalBytes: st.Blocks * bs,
		UsedBytes:  (st.Blocks - st.Bfree) * bs,
		FreeBytes:  st.Bavail * bs,
		FilesTotal: st.Files,
		FilesFree:  st.Ffree,
	}, true
}
