//go:build linux

package pgtest

import (
	"os"
	"strconv"
	"strings"
)

// pidSpaceID names the set of pids this process can see: the kernel's boot id
// (a fresh random UUID every boot, shared by every container on that kernel)
// plus the pid namespace this process lives in. Two processes with the same
// value probe each other's pids meaningfully; a container in another pid
// namespace, or another machine, gets a different one.
func pidSpaceID() (string, bool) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return "", false
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil || ns == "" {
		return "", false
	}
	return strings.TrimSpace(string(boot)) + "\x00" + ns, true
}

// processStartToken is pid's start time in clock ticks since boot, which
// together with the pid names one process for the life of the kernel.
func processStartToken(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	ticks, ok := linuxStartTicks(string(stat))
	if !ok {
		return "", false
	}
	return strconv.FormatInt(ticks, 10), true
}
