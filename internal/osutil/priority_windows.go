// Copyright (c) 2025 Reliant Labs
//go:build windows

package osutil

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// LowerChildPriority sets the just-started child to BELOW_NORMAL_PRIORITY_CLASS
// (see ChildNiceValue). Processes the child creates afterwards inherit the
// class. It must be called after the child has been started.
//
// Best-effort by contract: callers should log failures at debug level and
// never fail the spawn because of them.
func LowerChildPriority(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if childPriorityDisabled() {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("opening process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS); err != nil {
		return fmt.Errorf("SetPriorityClass(%d): %w", pid, err)
	}
	return nil
}
