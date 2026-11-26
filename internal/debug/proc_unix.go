//go:build !windows

package debug

import (
	"os/exec"
	"syscall"
)

// detachProcess starts dlv in its OWN SESSION so the debug server survives
// after forge exits — later `forge debug` invocations reconnect to the same
// session rather than starting a new one.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
