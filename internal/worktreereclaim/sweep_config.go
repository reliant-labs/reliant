// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"fmt"
	"strings"
	"time"
)

// SweepConfig is the daemon's worktree_reclaim config, parsed.
type SweepConfig struct {
	Mode         SweepMode
	Roots        []SweepRoot
	CleanIdle    time.Duration
	ApprovalIdle time.Duration
}

const (
	// DefaultSweepMode is dry-run for one release (owner decision,
	// 2026-11-08): it reports what it would reclaim, and the bytes.
	DefaultSweepMode = SweepDryRun
	minCleanIdle     = time.Hour
	minApprovalIdle  = 24 * time.Hour
)

// ParseSweepConfig turns the typed config strings (RELIANT_WORKTREE_RECLAIM*)
// into a SweepConfig. A value it cannot use falls back to its default, or is
// raised to its minimum, and is named in the returned warnings; it never
// stops the daemon.
func ParseSweepConfig(mode, roots, cleanIdle, approvalIdle string) (SweepConfig, []string) {
	var warnings []string
	cfg := SweepConfig{Mode: DefaultSweepMode, Roots: ParseSweepRoots(roots)}
	if strings.TrimSpace(mode) != "" {
		if m, ok := ParseSweepMode(mode); ok {
			cfg.Mode = m
		} else {
			warnings = append(warnings, fmt.Sprintf("RELIANT_WORKTREE_RECLAIM=%q is not off, dry-run or on; using %s", mode, DefaultSweepMode))
		}
	}
	var w string
	cfg.CleanIdle, w = parseSweepDuration("RELIANT_WORKTREE_RECLAIM_CLEAN_IDLE", cleanIdle, DefaultCleanIdle, minCleanIdle)
	if w != "" {
		warnings = append(warnings, w)
	}
	cfg.ApprovalIdle, w = parseSweepDuration("RELIANT_WORKTREE_RECLAIM_APPROVAL_IDLE", approvalIdle, DefaultApprovalIdle, minApprovalIdle)
	if w != "" {
		warnings = append(warnings, w)
	}
	return cfg, warnings
}

func parseSweepDuration(name, raw string, def, floor time.Duration) (time.Duration, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def, fmt.Sprintf("%s=%q is not a duration; using %s", name, raw, def)
	}
	if d < floor {
		return floor, fmt.Sprintf("%s=%s is below the %s minimum; using %s", name, d, floor, floor)
	}
	return d, ""
}
