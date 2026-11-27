//go:build linux

package resmetrics

// linux reads the real /proc and the cgroup2 mount; the sampler parses them
// itself (procfs.go, cgroup.go), so there are no native readers.
const (
	defaultProcRoot   = "/proc"
	defaultCgroupRoot = "/sys/fs/cgroup"

	nativeReaders = false
)

func readProcess(*ProcessStats, func(string, error)) {}
func readHost(*HostStats, bool, func(string, error)) {}
