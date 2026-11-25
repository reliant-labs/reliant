// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReportFile is the last pass's report in the reclaim state directory.
const ReportFile = "idle-report.json"

// Action is what a pass did, or in dry-run would do, with one directory.
type Action string

const (
	// ActionKept: the directory has a .keep file; nothing in it is touched.
	ActionKept Action = "kept"
	// ActionActive: used too recently for anything.
	ActionActive Action = "active"
	// ActionHeld: idle long enough, but a safety rule holds it (Reason).
	ActionHeld Action = "held"
	// ActionNothing: idle long enough, and nothing in it to remove.
	ActionNothing Action = "nothing"
	// ActionStripped: regenerable output (and, in scratch, clean pushed
	// worktrees) removed.
	ActionStripped Action = "stripped"
	// ActionRemoved: the whole scratch directory removed.
	ActionRemoved Action = "removed"
)

// Entry is one directory's outcome: a scratch directory or a worktree
// checkout.
type Entry struct {
	Path      string    `json:"path"`
	LastUse   time.Time `json:"last_use"`
	IdleHours int       `json:"idle_hours"`
	Action    Action    `json:"action"`
	// DryRun says the removals listed were not made.
	DryRun bool   `json:"dry_run,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Bytes is what was (or would be) freed.
	Bytes int64 `json:"bytes,omitempty"`
	// Removed lists what was (or would be) removed, relative to Path; "."
	// is the directory itself.
	Removed []string `json:"removed,omitempty"`
	// Held lists what the strip left, with why.
	Held []string `json:"held,omitempty"`
}

// Totals sums a report.
type Totals struct {
	ScratchDirs       int   `json:"scratch_dirs"`
	ScratchRemoved    int   `json:"scratch_removed"`
	ScratchStripped   int   `json:"scratch_stripped"`
	ScratchBytes      int64 `json:"scratch_bytes"`
	Worktrees         int   `json:"worktrees"`
	WorktreesStripped int   `json:"worktrees_stripped"`
	WorktreeBytes     int64 `json:"worktree_bytes"`
}

// Report is one pass.
type Report struct {
	Mode       Mode      `json:"mode"`
	DryRun     bool      `json:"dry_run"`
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// The thresholds the pass applied.
	ScratchStripIdle        string  `json:"scratch_strip_idle"`
	ScratchRemoveIdle       string  `json:"scratch_remove_idle"`
	WorktreeNodeModulesIdle string  `json:"worktree_node_modules_idle"`
	ScratchRoot             string  `json:"scratch_root"`
	WorktreeRoot            string  `json:"worktree_root"`
	Scratch                 []Entry `json:"scratch"`
	Worktrees               []Entry `json:"worktrees"`
	Totals                  Totals  `json:"totals"`
}

func (rep *Report) total() {
	t := Totals{ScratchDirs: len(rep.Scratch), Worktrees: len(rep.Worktrees)}
	for _, e := range rep.Scratch {
		t.ScratchBytes += e.Bytes
		switch e.Action {
		case ActionRemoved:
			t.ScratchRemoved++
		case ActionStripped:
			t.ScratchStripped++
		}
	}
	for _, e := range rep.Worktrees {
		t.WorktreeBytes += e.Bytes
		if e.Action == ActionStripped {
			t.WorktreesStripped++
		}
	}
	rep.Totals = t
}

// writeReport writes rep to stateDir.
func writeReport(stateDir string, rep Report) error {
	if stateDir == "" {
		return nil
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(stateDir, ReportFile), b)
}

// ReadReport reads the last pass's report from stateDir.
func ReadReport(stateDir string) (Report, error) {
	var rep Report
	b, err := os.ReadFile(filepath.Join(stateDir, ReportFile))
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return Report{}, fmt.Errorf("reading %s: %w", ReportFile, err)
	}
	return rep, nil
}

// HumanBytes renders n bytes in binary units.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
