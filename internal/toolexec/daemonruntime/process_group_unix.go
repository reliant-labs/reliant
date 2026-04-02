//go:build !windows

package daemonruntime

import (
	"os/exec"
	"syscall"
)

func setExecProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
