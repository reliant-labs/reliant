//go:build linux || darwin

package daemon

import "golang.org/x/sys/unix"

// AvailableBytes is the space an unprivileged write can still use on the
// filesystem that holds path, or would hold it once created (its nearest
// existing ancestor is read). ok is false when it cannot be read; callers then
// skip their check rather than refuse.
func AvailableBytes(path string) (uint64, bool) {
	p, err := nearestExisting(path)
	if err != nil {
		return 0, false
	}
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true //nolint:gosec,unconvert // Statfs_t field widths differ by GOOS/GOARCH
}
