//go:build !windows

package storage

import "golang.org/x/sys/unix"

// DiskSpace reports capacity on the physical host filesystem.
func DiskSpace(path string) (Disk, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	return Disk{Path: path, Available: uint64(s.Bavail) * uint64(s.Bsize), Capacity: uint64(s.Blocks) * uint64(s.Bsize)}, err
}
