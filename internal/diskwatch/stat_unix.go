// Copyright (c) 2025 Reliant Labs
//go:build !windows

package diskwatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// statFS reads the filesystem holding path. A path that does not exist yet (a
// cache directory nothing has written to) resolves to its nearest existing
// ancestor, which is where it will be created. resolved is the path read.
func statFS(path string) (st fsStat, resolved string, err error) {
	p := path
	var fi os.FileInfo
	for {
		fi, err = os.Stat(p)
		if err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return fsStat{}, "", err
		}
		p = parent
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(p, &sfs); err != nil {
		return fsStat{}, "", err
	}
	dev, _ := deviceOf(fi)
	return fsStat{
		Dev:    dev,
		Bsize:  uint64(sfs.Bsize),
		Blocks: uint64(sfs.Blocks),
		Bfree:  uint64(sfs.Bfree),
		Bavail: uint64(sfs.Bavail),
		Files:  uint64(sfs.Files),
		Ffree:  uint64(sfs.Ffree),
	}, p, nil
}

// allocatedBytes is what a file occupies on disk (du's measure), which for a
// sparse file or a filesystem with large blocks differs from its length.
func allocatedBytes(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
		return uint64(st.Blocks) * 512
	}
	if fi.Size() > 0 {
		return uint64(fi.Size())
	}
	return 0
}

// deviceOf is the st_dev of a stat result.
func deviceOf(fi fs.FileInfo) (uint64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true //nolint:unconvert // int32 on darwin
	}
	return 0, false
}
