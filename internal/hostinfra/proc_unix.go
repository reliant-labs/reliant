//go:build !windows

package hostinfra

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// detachProcess puts the child in its OWN SESSION so it outlives the command
// that started it.
//
// Without this the process dies with the launching shell's process group, and
// a host-infra service started by `forge env up` would not survive the command
// returning — which is the whole point of a host-infra service.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminateProcess asks the process to shut down cleanly (SIGTERM), giving it
// the chance to flush and release its port before it is killed outright.
func terminateProcess(pid int) error {
	if err := refuseLineage(pid); err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// killProcess terminates the process immediately (SIGKILL). Used only after a
// graceful stop has already timed out.
func killProcess(pid int) error {
	if err := refuseLineage(pid); err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// processAlive reports whether pid is a live process (signal-0 probe; EPERM
// means it exists but belongs to someone else).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// isExecutableFile reports whether the file has any exec bit set.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode()&0o111 != 0
}

// removeShmSegment marks the SysV segment id for removal (ipcrm -m); it is
// freed once the last attached process detaches. Needs ipcrm (macOS, Linux).
func removeShmSegment(id int) {
	_ = exec.Command("ipcrm", "-m", strconv.Itoa(id)).Run()
}

// isProcessGone reports whether err from terminateProcess means the process
// had already exited.
func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
