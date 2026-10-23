//go:build darwin

package procguard

import "golang.org/x/sys/unix"

// osLookup asks the kernel directly (sysctl kern.proc.pid.<pid>), which
// returns the parent and process group without forking ps.
func osLookup() Lookup {
	return func(pid int) (Entry, bool) {
		kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
			return Entry{}, false
		}
		return Entry{PPID: int(kp.Eproc.Ppid), PGID: int(kp.Eproc.Pgid)}, true
	}
}
