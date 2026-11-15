//go:build windows

package hostinfra

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// detachProcess makes the child survive the command that started it.
//
// Windows has no setsid. The equivalent is CREATE_NEW_PROCESS_GROUP, which
// detaches the child from the parent's console process group so a Ctrl-C (or
// the parent exiting) does not take it down with them. DETACHED_PROCESS
// additionally gives it no console at all, which is right for a background
// service whose output already goes to a log file.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// terminateProcess asks the process to stop.
//
// IMPORTANT SEMANTIC DIFFERENCE FROM UNIX: Windows has no SIGTERM, and
// os.Process.Signal only supports Kill there. So this is NOT a graceful stop
// the way SIGTERM is — the process does not get to run shutdown handlers.
//
// That is acceptable for the callers here for the same reason the unix path
// is willing to escalate to SIGKILL after a timeout: the state that matters
// is in postgres and is already durable, and leaving the process running
// would hold its port against the next start. Callers still poll for exit
// afterwards, so the control flow is identical on both platforms.
func terminateProcess(pid int) error {
	if err := refuseLineage(pid); err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// killProcess terminates the process immediately. On Windows this is the same
// operation as terminateProcess (see its comment) — kept as a distinct name so
// the shared call sites read the same on every platform.
func killProcess(pid int) error {
	return terminateProcess(pid)
}

// processAlive reports whether pid names a running process. Only
// ERROR_INVALID_PARAMETER from OpenProcess means "no such pid"; any other
// failure (access denied, ...) means it exists. Liveness is
// WaitForSingleObject(h, 0): WAIT_TIMEOUT means still running, whereas an exit
// code of 259 cannot be told apart from STILL_ACTIVE.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return true
	}
	return ev != windows.WAIT_OBJECT_0
}

// isExecutableFile: Windows has no exec bit; a runnable binary is a regular
// file with an .exe extension.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(info.Name()), ".exe")
}

// removeShmSegment is a no-op: postgres on Windows uses no SysV shared memory.
func removeShmSegment(int) {}

// isProcessGone reports whether err from terminateProcess means the process
// had already exited. A pid that died between the liveness check and the kill
// fails OpenProcess with ERROR_INVALID_PARAMETER.
func isProcessGone(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, os.ErrProcessDone)
}
