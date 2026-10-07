// Copyright (c) 2025 Reliant Labs
//go:build windows

package worktreereclaim

import (
	"os"

	"golang.org/x/sys/windows"
)

// DiskUsage reports free and total bytes of the volume holding path.
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
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return nil, err
	}
	return &Disk{Path: path, FreeBytes: int64(free), TotalBytes: int64(total)}, nil
}
