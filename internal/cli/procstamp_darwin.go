//go:build darwin

package cli

import (
	"time"

	"golang.org/x/sys/unix"
)

// readProcStamp reads pid's process group and start time from the kernel
// (sysctl kern.proc.pid.<pid>) — no fork, microsecond start time.
func readProcStamp(pid int) (procStamp, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return procStamp{}, false
	}
	st := kp.Proc.P_starttime
	return procStamp{pgid: int(kp.Eproc.Pgid), start: st.Sec*1_000_000 + int64(st.Usec)}, true
}

// startMark is now, on the clock P_starttime is recorded in (wall clock, in
// microseconds).
func startMark() (int64, bool) {
	return time.Now().UnixMicro(), true
}
