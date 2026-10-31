// Copyright (c) 2026 Reliant Labs
//go:build windows

package proctree

import (
	"os/exec"
	"syscall"
)

// setProcessGroup gives the command a process group of its own
// (CREATE_NEW_PROCESS_GROUP), keeping any other creation flag already set.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
