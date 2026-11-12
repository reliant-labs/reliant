//go:build linux

package procguard

import (
	"os"
	"strconv"
)

// osLookup reads /proc/<pid>/stat, which carries both the parent and the
// process group in one read and needs no external tool.
func osLookup() Lookup {
	return func(pid int) (Entry, bool) {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return Entry{}, false
		}
		return parseProcStat(data)
	}
}
