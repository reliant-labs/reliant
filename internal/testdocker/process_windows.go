//go:build windows

package testdocker

import "os"

// processAlive reports whether a process with this pid exists on this host:
// on Windows, os.FindProcess opens a handle to it and fails when there is none.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
