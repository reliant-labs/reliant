// Copyright (c) 2025 Reliant Labs
//go:build linux

package osutil

import (
	"fmt"
	"os"
	"strconv"
)

// AdjustChildOOMScore raises the OOM-killer badness score of the process
// with the given PID by writing ChildOOMScoreAdj to
// /proc/<pid>/oom_score_adj. It must be called after the child has been
// started (the PID must exist). Raising a score is unprivileged, so this
// works for the non-root daemon user.
//
// Best-effort by contract: callers should log failures at debug/warn level
// and never fail the spawn because of them (the child may already have
// exited, or /proc may be restricted in exotic sandboxes).
func AdjustChildOOMScore(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	if err := os.WriteFile(path, []byte(strconv.Itoa(ChildOOMScoreAdj)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
