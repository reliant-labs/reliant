// Copyright (c) 2025 Reliant Labs
//go:build windows

package tools

import "os/exec"

func setProcAttr(cmd *exec.Cmd) {
	// On Windows, we don't need to set process group
}

func getExitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
