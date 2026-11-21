//go:build !windows

package testdocker

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this pid exists on this host.
// Signal 0 checks existence without delivering anything; EPERM means it exists
// but belongs to another user. A pid reused by an unrelated process reads as
// alive, which errs toward keeping a leftover until that process exits.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
