// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"fmt"
	"os"
)

// ValidateWorkingDir reports why a requested working directory cannot be used
// to spawn a child process, or nil if it can.
//
// This exists because the kernel's own answer is misattributed. Go performs the
// chdir inside the forked child, so a missing directory surfaces as
// "fork/exec /bin/bash: no such file or directory" — naming the SHELL, not the
// directory that is actually missing. A caller reading that concludes its shell
// is gone and goes looking in entirely the wrong place. Checking before the
// spawn lets the failure name the thing that is actually wrong.
//
// It lives here, next to AdjustChildOOMScore, because every path that spawns a
// child on a user's behalf needs it: the local executor, the daemon's exec.run
// handler, and the background process manager.
func ValidateWorkingDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("working directory does not exist: %s", dir)
		}
		return fmt.Errorf("working directory is unusable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory is not a directory: %s", dir)
	}
	return nil
}
