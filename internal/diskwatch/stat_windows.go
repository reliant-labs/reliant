// Copyright (c) 2025 Reliant Labs
//go:build windows

package diskwatch

import (
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// statFS reads the volume holding path (see the unix version). Windows has no
// st_dev, so the volume name stands in for it; inodes are not reported.
func statFS(path string) (st fsStat, resolved string, err error) {
	p := path
	for {
		if _, err = os.Stat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return fsStat{}, "", err
		}
		p = parent
	}
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return fsStat{}, "", err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return fsStat{}, "", err
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(filepath.VolumeName(p))))
	return fsStat{Dev: h.Sum64(), Bsize: 1, Blocks: total, Bfree: totalFree, Bavail: free}, p, nil
}

func allocatedBytes(fi fs.FileInfo) uint64 {
	if fi.Size() > 0 {
		return uint64(fi.Size())
	}
	return 0
}

// deviceOf is unknown on Windows; walks do not stop at mount points there.
func deviceOf(fs.FileInfo) (uint64, bool) { return 0, false }
