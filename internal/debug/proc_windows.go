//go:build windows

package debug

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// detachProcess starts dlv detached so the debug server survives after forge
// exits — later `forge debug` invocations reconnect to the same session.
//
// Windows has no setsid. CREATE_NEW_PROCESS_GROUP detaches the child from the
// parent's console group so a Ctrl-C to forge does not also kill dlv, and
// DETACHED_PROCESS gives it no console — appropriate here because the caller
// already redirects dlv's stdio to os.DevNull.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
