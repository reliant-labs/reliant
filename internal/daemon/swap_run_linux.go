// Copyright (c) 2026 Reliant Labs

//go:build linux

package daemon

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitExitNoReap blocks until pid has exited, leaving it unreaped: whoever
// reaps it decides who gets its exit status (swap_run.go).
func waitExitNoReap(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
