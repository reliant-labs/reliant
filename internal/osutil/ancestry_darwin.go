// Copyright (c) 2025 Reliant Labs
//go:build darwin

package osutil

import "golang.org/x/sys/unix"

// ParentPID returns pid's parent process id, from the kernel's kinfo_proc.
func ParentPID(pid int) (int, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(info.Eproc.Ppid), nil
}
