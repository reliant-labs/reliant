// Copyright (c) 2025 Reliant Labs
//go:build windows

package diskwatch

import (
	"os/exec"
	"syscall"
)

func prepareReaperCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
