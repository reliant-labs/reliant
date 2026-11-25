//go:build !windows

package pgtest

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const pgCtlBinary = "pg_ctl"

// processAlive reports whether pid names a live process. EPERM means the
// process exists but belongs to another user, which still counts as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	serr := proc.Signal(syscall.Signal(0))
	return serr == nil || errors.Is(serr, syscall.EPERM)
}

// gracefulStop asks postgres for a fast shutdown (SIGINT), letting it release
// its IPC on the way out. It reports whether the signal was delivered.
func gracefulStop(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.SIGINT) == nil
}

func forceKill(pid int) {
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGKILL)
	}
}

// removeShmSegment marks the SysV segment id for removal (ipcrm -m); it is
// freed once the last attached process detaches. Needs ipcrm (macOS, Linux).
func removeShmSegment(id int) {
	_ = exec.Command("ipcrm", "-m", strconv.Itoa(id)).Run()
}
