//go:build linux

package hostslot

import "os"

func totalMemory() (uint64, string) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, ""
	}
	return meminfoTotal(data), "/proc/meminfo"
}

func platformCgroupLimits() (cgroupLimit, bool) {
	return readCgroupV2("/sys/fs/cgroup", "/proc/self/cgroup")
}
