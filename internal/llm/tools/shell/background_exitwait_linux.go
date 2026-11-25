// Copyright (c) 2026 Reliant Labs
//go:build linux

package shell

import (
	"errors"

	"golang.org/x/sys/unix"
)

// fileOutputSupported: a child can write its output to files and be waited
// for without being reaped (waitExited), so it can outlive this image.
const fileOutputSupported = true

// waitExited blocks until pid, a child of this process, has exited, WITHOUT
// reaping it (WNOWAIT): its exit status stays for whoever reaps it — this
// image, or the next one after a self-update's exec.
func waitExited(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
