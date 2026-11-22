//go:build linux || darwin

package resmetrics

import "syscall"

// volumesSupported: statVolume reads filesystems here.
const volumesSupported = true

// statVolume returns the device of path and the space used and available on
// its filesystem.
func statVolume(path string) (dev, used, free uint64, err error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, 0, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(fs.Bsize)                                                     //nolint:gosec,unconvert // a block size is positive; int64 on linux, uint32 on darwin
	return uint64(st.Dev), (fs.Blocks - fs.Bfree) * bsize, fs.Bavail * bsize, nil //nolint:gosec,unconvert // Dev is uint64 on linux, int32 on darwin
}
