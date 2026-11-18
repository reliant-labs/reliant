//go:build windows

package cli

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// detachFromParent gives the child its own process group and no console, so
// the parent exiting or a Ctrl-C does not take it down with it.
func detachFromParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
