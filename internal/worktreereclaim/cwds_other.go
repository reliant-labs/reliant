// Copyright (c) 2025 Reliant Labs
//go:build !linux && !windows

package worktreereclaim

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ProcessCwds lists every process's working directory via lsof. It is the one
// portable probe on macOS (no /proc), and it can take seconds on a busy host,
// so it is bounded; any failure or timeout is an error, because "could not
// tell" must never read as "nothing is in use".
func ProcessCwds(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-Fn", "-w", "-n", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			cwds = append(cwds, line[1:])
		}
	}
	if len(cwds) == 0 {
		return nil, fmt.Errorf("lsof reported no processes")
	}
	return cwds, nil
}
