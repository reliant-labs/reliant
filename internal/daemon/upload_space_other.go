//go:build !linux && !darwin && !windows

package daemon

// AvailableBytes is not known on this platform; callers skip their check.
func AvailableBytes(string) (uint64, bool) { return 0, false }
