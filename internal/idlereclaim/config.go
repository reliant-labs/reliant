// Copyright (c) 2026 Reliant Labs

// Package idlereclaim frees the disk of directories nobody is using, in the
// two places nothing else cleans:
//
//   - the scratch directories the system prompt gives every chat
//     (~/.reliant/scratch/<chat>/). Agents are told to keep briefings,
//     clones and background output there because it survives a restart, so
//     on a busy build machine it grew to 110 GiB in four days. After
//     ScratchStripIdle with no use, the regenerable output in one
//     (node_modules, build output) and the clean, pushed git worktrees in it
//     are removed; after ScratchRemoveIdle the whole directory is. A `.keep`
//     file at its top keeps it whole.
//   - the node_modules of linked worktrees under the worktree root that are
//     kept for another reason (uncommitted or unpushed work, a chat still
//     bound to them) and have been idle for WorktreeNodeModulesIdle. Nothing
//     else in such a worktree is touched.
//
// A sub-agent's directory in a chat's (<chat>/<agent id>/) has the same
// lifecycle on its own clock, and inside a directory still in use each
// checkout is stripped once it alone has gone unused (scratch.go).
//
// It runs in the daemon, the only process that sees the filesystem, on its
// own schedule (Reclaimer.Run), and sooner, with a day as the strip floor,
// while the disk is under pressure (pressure.go). Every removal is logged and recorded in a
// report (ReportFile); dry-run computes the same decisions and removes
// nothing. The safety rules are the worktree reclaim's: an age floor, no
// live process with its cwd, executable, open or mapped files inside, the
// daemon's admission lease for the path, and the regenerable-output
// inventory's holds (worktreereclaim.InspectRegenerable).
//
//forge:exclude-contract: daemon-side filesystem and git passes with one implementation; what only the daemon has (lease, process inspection, use logs) is injected as funcs in Deps, and tests run against real temp directories and git repos
package idlereclaim

import (
	"fmt"
	"strings"
	"time"
)

// Mode is how the idle reclaim runs (RELIANT_IDLE_RECLAIM).
type Mode string

const (
	// ModeAuto runs it wherever the daemon's storage reaper runs (managed
	// daemons, and any machine that set RELIANT_STORAGE_REAPER=on) and
	// nowhere else.
	ModeAuto Mode = "auto"
	// ModeOn runs it whatever the storage reaper does.
	ModeOn Mode = "on"
	// ModeDryRun decides and reports, and removes nothing, whatever the
	// storage reaper does.
	ModeDryRun Mode = "dry-run"
	// ModeOff never runs it.
	ModeOff Mode = "off"
)

const (
	// DefaultScratchStripIdle is how long a scratch directory must have gone
	// unused before its regenerable output and clean, pushed worktrees go.
	DefaultScratchStripIdle = 48 * time.Hour
	// DefaultScratchRemoveIdle is how long before the whole directory goes.
	DefaultScratchRemoveIdle = 7 * 24 * time.Hour
	// DefaultWorktreeNodeModulesIdle is how long a kept worktree must have
	// gone unused before its node_modules go.
	DefaultWorktreeNodeModulesIdle = 72 * time.Hour

	minScratchStripIdle        = 24 * time.Hour
	minScratchRemoveIdle       = 72 * time.Hour
	minWorktreeNodeModulesIdle = 24 * time.Hour
)

// Config is the daemon's idle_reclaim config, parsed.
type Config struct {
	Mode                    Mode
	ScratchStripIdle        time.Duration
	ScratchRemoveIdle       time.Duration
	WorktreeNodeModulesIdle time.Duration
}

// ParseConfig turns the typed config strings (RELIANT_IDLE_RECLAIM*) into a
// Config. A value it cannot use falls back to its default, or is raised to
// its minimum, and is named in the returned warnings; it never stops the
// daemon. The whole-directory age is never below the strip age.
func ParseConfig(mode, stripIdle, removeIdle, nodeModulesIdle string) (Config, []string) {
	var warnings []string
	cfg := Config{Mode: ModeAuto}
	switch m := Mode(strings.ToLower(strings.TrimSpace(mode))); m {
	case "":
	case ModeAuto, ModeOn, ModeDryRun, ModeOff:
		cfg.Mode = m
	default:
		warnings = append(warnings, fmt.Sprintf("RELIANT_IDLE_RECLAIM=%q is not auto, on, dry-run or off; using auto", mode))
	}
	var w string
	cfg.ScratchStripIdle, w = parseDuration("RELIANT_IDLE_RECLAIM_SCRATCH_STRIP_IDLE", stripIdle, DefaultScratchStripIdle, minScratchStripIdle)
	warnings = appendIf(warnings, w)
	cfg.ScratchRemoveIdle, w = parseDuration("RELIANT_IDLE_RECLAIM_SCRATCH_REMOVE_IDLE", removeIdle, DefaultScratchRemoveIdle, minScratchRemoveIdle)
	warnings = appendIf(warnings, w)
	if cfg.ScratchRemoveIdle < cfg.ScratchStripIdle {
		warnings = append(warnings, fmt.Sprintf("RELIANT_IDLE_RECLAIM_SCRATCH_REMOVE_IDLE=%s is below the strip idle %s; using %s", cfg.ScratchRemoveIdle, cfg.ScratchStripIdle, cfg.ScratchStripIdle))
		cfg.ScratchRemoveIdle = cfg.ScratchStripIdle
	}
	cfg.WorktreeNodeModulesIdle, w = parseDuration("RELIANT_IDLE_RECLAIM_WORKTREE_NODE_MODULES_IDLE", nodeModulesIdle, DefaultWorktreeNodeModulesIdle, minWorktreeNodeModulesIdle)
	warnings = appendIf(warnings, w)
	return cfg, warnings
}

// Effective is what the mode means on this daemon: whether passes run, and
// whether they only report. reaperEnabled is whether the daemon's storage
// reaper runs here (diskwatch.ReaperConfig.Enabled).
func (c Config) Effective(reaperEnabled bool) (run, dryRun bool) {
	switch c.Mode {
	case ModeOn:
		return true, false
	case ModeDryRun:
		return true, true
	case ModeOff:
		return false, false
	default: // auto, and the zero value
		return reaperEnabled, false
	}
}

func appendIf(list []string, s string) []string {
	if s == "" {
		return list
	}
	return append(list, s)
}

func parseDuration(name, raw string, def, floor time.Duration) (time.Duration, string) {
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
