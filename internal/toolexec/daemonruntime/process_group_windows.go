//go:build windows

package daemonruntime

import (
	"os/exec"
	"syscall"
)

func setExecProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
