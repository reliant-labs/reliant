//go:build darwin

package pgtest

import (
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// pidSpaceID names the set of pids this process can see. macOS has no pid
// namespaces, so it is the boot: kern.bootsessionuuid is a fresh UUID every
// boot, which also tells two machines apart.
func pidSpaceID() (string, bool) {
	id, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil || strings.TrimSpace(id) == "" {
		return "", false
	}
	return strings.TrimSpace(id), true
}

// processStartToken is pid's start time as the kernel recorded it at fork
// (kinfo_proc p_starttime), which together with the pid names one process.
// It is a stored value, so a later wall-clock step does not move it.
func processStartToken(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return "", false
	}
	tv := kp.Proc.P_starttime
	if tv.Sec <= 0 {
		return "", false
	}
	return strconv.FormatInt(int64(tv.Sec), 10) + "." + strconv.FormatInt(int64(tv.Usec), 10), true
}
