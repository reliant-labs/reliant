//go:build !windows

package doctor

import (
	"io/fs"
	"syscall"
)

// allocatedBytes reports how many bytes of the filesystem a file actually
// occupies — st_blocks*512, the number `du` prints.
//
// This is NOT fi.Size(). Docker Desktop's Docker.raw is a sparse file whose
// apparent size is a fixed 1 TiB from the day it is created: on the machine
// this check was written for, `ls` reported 1,099,511,627,776 bytes while
// `du` reported 90 GiB. st_blocks counts only the blocks the filesystem has
// committed, which is exactly the quantity the host gave up and the only
// one worth reporting.
//
// The 512 multiplier is the POSIX definition of st_blocks and is fixed
// regardless of the filesystem's own block size.
func allocatedBytes(fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	if st.Blocks < 0 {
		return 0, false
	}
	return uint64(st.Blocks) * 512, true
}
