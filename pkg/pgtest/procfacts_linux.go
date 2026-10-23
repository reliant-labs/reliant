//go:build linux

package pgtest

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// linuxClockTicks is USER_HZ, which is 100 on every Linux ABI Go supports.
const linuxClockTicks = 100

// readProcFacts reads pid's executable from /proc/<pid>/exe and its start
// time from /proc/<pid>/stat plus btime in /proc/stat.
func readProcFacts(pid int) procFacts {
	var f procFacts
	if pid <= 0 {
		return f
	}
	dir := "/proc/" + strconv.Itoa(pid)
	if exe, err := os.Readlink(dir + "/exe"); err == nil && exe != "" {
		f.exe, f.exeOK = exe, true
	}
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return f
	}
	sys, err := os.ReadFile("/proc/stat")
	if err != nil {
		return f
	}
	for _, line := range strings.Split(string(sys), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, perr := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); perr == nil {
				f.start, f.startOK = linuxStartTime(string(stat), time.Unix(sec, 0), linuxClockTicks)
			}
			break
		}
	}
	return f
}
