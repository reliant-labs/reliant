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

// darwinSZOMB is p_stat's zombie state (<sys/proc.h>: SIDL 1, SRUN 2,
// SSLEEP 3, SSTOP 4, SZOMB 5); golang.org/x/sys does not export it.
const darwinSZOMB = 5

// processExited reports whether pid is a zombie: it has exited and holds
// nothing, but its parent has not reaped it, so kill(2) still finds it.
// False when pid is not in the table at all (gone is the caller's
// question, answered by kill) or cannot be read.
func processExited(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return false
	}
	return kp.Proc.P_stat == darwinSZOMB
}

// startMark is now, on the clock P_starttime is recorded in (wall clock, in
// microseconds).
func startMark() (int64, bool) {
	return time.Now().UnixMicro(), true
}
