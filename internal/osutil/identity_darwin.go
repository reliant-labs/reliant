// Copyright (c) 2026 Reliant Labs
//go:build darwin

package osutil

import "golang.org/x/sys/unix"

// IdentifyProcess reads pid's parent, process group, start time and whether
// it is a zombie from the kernel's kinfo_proc. ok is false when there is no
// such process (or it was reaped).
func IdentifyProcess(pid int) (ProcessIdentity, bool) {
	if pid <= 0 {
		return ProcessIdentity{}, false
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(p.Proc.P_pid) != pid {
		return ProcessIdentity{}, false
	}
	return ProcessIdentity{
		PPID:   int(p.Eproc.Ppid),
		PGID:   int(p.Eproc.Pgid),
		Start:  kinfoStart(p),
		Exited: p.Proc.P_stat == szomb,
	}, true
}
