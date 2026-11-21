//go:build windows

package cli

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// startBackgroundProcess prepares a child that must outlive forge's terminal:
// its own process group plus DETACHED_PROCESS, so closing the console window
// does not take the stack down with it.
func startBackgroundProcess(cmd *exec.Cmd) {
	startInOwnProcessGroup(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.DETACHED_PROCESS
}
