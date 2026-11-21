//go:build linux

package resmetrics

import "syscall"

// linux reads the real /proc and the cgroup2 mount.
const (
	defaultProcRoot   = "/proc"
	defaultCgroupRoot = "/sys/fs/cgroup"

	// volumesSupported: statVolume reads filesystems here.
	volumesSupported = true
)

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
	bsize := uint64(fs.Bsize)                                                     //nolint:gosec // a block size is positive
	return uint64(st.Dev), (fs.Blocks - fs.Bfree) * bsize, fs.Bavail * bsize, nil //nolint:unconvert // Dev is uint32 on some arches
}
