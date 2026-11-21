//go:build windows

package pgtest

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The embedded-postgres Windows archive ships pg_ctl.exe.
const pgCtlBinary = "pg_ctl.exe"

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

// gracefulStop has no signal equivalent on Windows (SIGINT cannot be sent to
// another process); graceful postgres shutdown goes through pg_ctl instead, so
// callers fall through to forceKill.
func gracefulStop(pid int) bool { return false }

func forceKill(pid int) {
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}

// removeShmSegment is a no-op: postgres on Windows uses no SysV shared memory.
func removeShmSegment(int) {}
