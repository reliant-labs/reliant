// Copyright (c) 2025 Reliant Labs
//go:build linux

package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// ProcessCwds lists the working directory of every process whose /proc entry
// is readable. A process it cannot read (another user's) is skipped: such a
// process cannot be inside a worktree this user created, except by root.
func ProcessCwds(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var cwds []string
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if dir, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd")); err == nil {
			cwds = append(cwds, dir)
		}
	}
	return cwds, nil
}
