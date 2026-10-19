// Copyright (c) 2025 Reliant Labs
//go:build !windows

package diskwatch

import (
	"os/exec"
	"syscall"
)

// prepareReaperCmd puts the reaper in its own process group (osutil's priority
// helpers act on the group) and makes the budget's cancel a SIGTERM to the
// whole group, which forge's pass handles as a clean stop; WaitDelay kills it
// if it does not.
func prepareReaperCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
