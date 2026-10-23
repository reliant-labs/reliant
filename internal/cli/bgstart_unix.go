//go:build !windows

package cli

import "os/exec"

// startBackgroundProcess prepares a child that must outlive forge's terminal.
func startBackgroundProcess(cmd *exec.Cmd) {
	startInOwnProcessGroup(cmd)
}
