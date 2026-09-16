// Copyright (c) 2025 Reliant Labs
//go:build windows

package gitutil

import (
	"os/exec"
	"syscall"
)

// setCommandProcessGroup gives git its own process group, mirroring the Unix
// build. Windows has no SIGTERM, so cancellation still kills (see
// osutil.terminateGracefully); the group is set anyway to match the other
// exec paths in this repository and to keep the two builds structurally the
// same.
func setCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
