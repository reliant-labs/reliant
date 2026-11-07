// Copyright (c) 2026 Reliant Labs
//go:build darwin

package osutil

import "golang.org/x/sys/unix"

// szomb is the kernel's p_stat for a zombie (SZOMB in <sys/proc.h>); x/sys
// does not export it.
const szomb = 5

// GroupMembers lists the live processes of process group pgid. Zombies are
// not live: a zombie runs nothing, and one whose reaper never comes would
// otherwise look like a workload that survived SIGKILL forever. ok is false
// only where the process table cannot be read.
func GroupMembers(pgid int) (members []GroupMember, ok bool) {
	if pgid <= 0 {
		return nil, true
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, false
	}
	for i := range procs {
		p := &procs[i]
		if int(p.Eproc.Pgid) == pgid && p.Proc.P_stat != szomb {
			members = append(members, GroupMember{PID: int(p.Proc.P_pid), Start: kinfoStart(p)})
		}
	}
	return members, true
}

// GroupOwned reports whether any of members is still alive, the same process
// (same start time), and still in group pgid — proof that pgid is still the
// group they were recorded in.
func GroupOwned(pgid int, members []GroupMember) bool {
	for _, m := range members {
		p, err := unix.SysctlKinfoProc("kern.proc.pid", m.PID)
		if err != nil {
			continue
		}
		if int(p.Proc.P_pid) == m.PID && int(p.Eproc.Pgid) == pgid &&
			kinfoStart(p) == m.Start && p.Proc.P_stat != szomb {
			return true
		}
	}
	return false
}

// kinfoStart is the process start time in microseconds since the epoch, the
// identity that tells a pid's current holder from an earlier one.
func kinfoStart(p *unix.KinfoProc) uint64 {
	t := p.Proc.P_starttime
	return uint64(t.Sec)*1_000_000 + uint64(t.Usec)
}
