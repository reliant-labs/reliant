//go:build !linux && !darwin

package hostslot

// Elsewhere memory is not read: the default falls back to CPUs alone.
func totalMemory() (uint64, string) { return 0, "" }

func platformCgroupLimits() (cgroupLimit, bool) { return cgroupLimit{}, false }
