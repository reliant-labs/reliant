// Copyright (c) 2026 Reliant Labs
//go:build darwin

package shell

import "golang.org/x/sys/unix"

// isZombie reports whether pid is dead and awaiting its reaper.
func isZombie(pid int) bool {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && int(p.Proc.P_pid) == pid && p.Proc.P_stat == 5 // SZOMB
}
