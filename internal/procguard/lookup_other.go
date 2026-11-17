//go:build !darwin && !linux && !windows

package procguard

import (
	"os/exec"
	"strconv"
	"strings"
)

// osLookup falls back to ps on the remaining Unixes (the BSDs), which all
// support -o ppid=,pgid=. A missing ps leaves only the kernel's answer for
// this process's own parent (see Self).
func osLookup() Lookup {
	return func(pid int) (Entry, bool) {
		out, err := exec.Command("ps", "-o", "ppid=,pgid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return Entry{}, false
		}
		f := strings.Fields(string(out))
		if len(f) != 2 {
			return Entry{}, false
		}
		ppid, err1 := strconv.Atoi(f[0])
		pgid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return Entry{}, false
		}
		return Entry{PPID: ppid, PGID: pgid}, true
	}
}
