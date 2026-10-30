// Copyright (c) 2026 Reliant Labs
//go:build !windows

package proctree

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in a process group of its own, keeping any
// other attribute a caller already set (credsandbox's namespaces).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
