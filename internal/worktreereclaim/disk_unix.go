// Copyright (c) 2025 Reliant Labs
//go:build !windows

package worktreereclaim

import (
	"os"

	"golang.org/x/sys/unix"
)

// DiskUsage reports free and total bytes of the volume holding path, walking up
// to the nearest existing ancestor so it works before the directory exists.
// Free is what an unprivileged process can use.
func DiskUsage(path string) (*Disk, error) {
	p := path
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		parent := parentDir(p)
		if parent == p {
			break
		}
		p = parent
	}
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return nil, err
	}
	bsize := int64(st.Bsize)
	return &Disk{Path: path, FreeBytes: int64(st.Bavail) * bsize, TotalBytes: int64(st.Blocks) * bsize}, nil
}
