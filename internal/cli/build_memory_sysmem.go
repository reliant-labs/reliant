package cli

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// systemTotalMemoryBytes returns total physical memory in bytes, or 0 when it
// cannot be determined. Implemented via /proc/meminfo (Linux) with no external
// dependency — Linux is the platform where the cgroup-constrained build case
// (cloud daemon pods) actually runs, so the memory-aware path is meaningful
// there; on other OSes this returns 0 and the build keeps its parallel
// default (the cgroup probe already returns nothing off-Linux too).
func systemTotalMemoryBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	// Read-only probe of /proc/meminfo: a Close error carries no
	// information and there is nothing to recover.
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		// Format: "MemTotal:       32813284 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
