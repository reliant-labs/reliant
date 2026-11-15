//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachFromParent starts the child in its own session, so it is not in the
// terminal's process group: a Ctrl-C, or the parent exiting, does not take it
// down with it.
func detachFromParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
