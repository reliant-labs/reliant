//go:build darwin

package hostslot

import "golang.org/x/sys/unix"

func totalMemory() (uint64, string) {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, ""
	}
	return n, "hw.memsize"
}

func platformCgroupLimits() (cgroupLimit, bool) { return cgroupLimit{}, false }
