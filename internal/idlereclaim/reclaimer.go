// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The daemon's two scratch use logs (worktreereclaim.UseLog over the scratch
// root), in the reclaim state directory: work naming a path in a scratch
// directory, and each chat's last tool call keyed by its directory.
const (
	ScratchUseFile  = "scratch-use.json"
	ScratchChatFile = "scratch-chat.json"
)

const (
	// PassInterval is how often a pass runs. The thresholds are days, so a
	// pass every few hours removes a directory within hours of its due time;
	// a pass walks every scratch directory and every worktree checkout.
	PassInterval = 3 * time.Hour
	// firstPassDelay keeps the first pass off the daemon's startup.
	firstPassDelay = 10 * time.Minute
)

// Deps is what a pass reads besides the filesystem. A nil func is the
// conservative answer: no recorded use, and no lease (so nothing is removed).
type Deps struct {
	Now func() time.Time
	// ProcessUse reports a live process using any of paths (cwd,
	// executable, open or mapped files); any error holds the paths.
	// worktreereclaim.CheckProcessUse.
	ProcessUse func(ctx context.Context, paths []string) error
	// Acquire takes the daemon's admission lease over paths; nil in a
	// process that has none (the CLI), which then never removes anything.
	Acquire func(ctx context.Context, paths []string) (Lease, error)
	// Inspect inventories a regenerable directory: its bytes and newest
	// mtime, or an error that holds it (worktreereclaim.InspectRegenerable).
	Inspect func(ctx context.Context, dir string) (int64, time.Time, error)
	// NodeModules lists a checkout's git-ignored node_modules, relative
	// (worktreereclaim.NodeModulesTargets).
	NodeModules func(ctx context.Context, checkout string) []string
	// ScratchUse is the last time daemon work named a path in a scratch
	// directory (a file read, a command's cwd), which leaves no mtime.
	ScratchUse func(path string) time.Time
	// ChatUse is the last tool call of the chat a scratch directory is
	// named for, as this daemon saw it.
	ChatUse func(path string) time.Time
	// WorktreeUse is the last time daemon work named a path in a worktree
	// (worktreereclaim.UseLog.LastUse).
	WorktreeUse func(path string) time.Time
}

// Reclaimer runs the idle reclaim over one scratch root and one worktree root.
//
//forge:contract
type Reclaimer struct {
	Config       Config
	ScratchRoot  string
	WorktreeRoot string
	// StateDir is where the report and the removal record live
	// (~/.reliant/reclaim); "" writes neither.
	StateDir string
	// WorktreeSweepOn says the machine-wide worktree reclaim runs in mode on
	// (RELIANT_WORKTREE_RECLAIM=on): it then owns every linked worktree's
	// removal, captures it first, and the scratch strip removes none.
	WorktreeSweepOn bool
	// Records is the removal record the command note reads.
	Records *Records
	Deps

	mu sync.Mutex // one pass at a time
}

// DefaultScratchRoot is ~/.reliant/scratch, the directory the system prompt
// gives each chat a directory under.
func DefaultScratchRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reliant", "scratch"), nil
}

// NewReclaimer returns a reclaimer over the default roots with the default
// dependencies: the ones a CLI process can have (no lease, no recorded use).
// The daemon fills in Acquire and the use logs.
func NewReclaimer(cfg Config) (*Reclaimer, error) {
	scratch, err := DefaultScratchRoot()
	if err != nil {
		return nil, err
	}
	worktrees, err := worktreereclaim.DefaultRoot()
	if err != nil {
		return nil, err
	}
	state, err := worktreereclaim.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	return &Reclaimer{
		Config: cfg, ScratchRoot: scratch, WorktreeRoot: worktrees, StateDir: state,
		Records: NewRecords(filepath.Join(state, RecordFile)),
		Deps: Deps{
			ProcessUse:  worktreereclaim.CheckProcessUse,
			Inspect:     worktreereclaim.InspectRegenerable,
			NodeModules: worktreereclaim.NodeModulesTargets,
		},
	}, nil
}

func (r *Reclaimer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func lastOf(f func(string) time.Time, path string) time.Time {
	if f == nil {
		return time.Time{}
	}
	return f(path)
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func (r *Reclaimer) inUse(ctx context.Context, path string) error {
	if r.ProcessUse == nil {
		return fmt.Errorf("process inspection unavailable")
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.ProcessUse(probe, []string{path})
}

// Run runs a pass every PassInterval until ctx is done, when the mode says
// passes run on this daemon. reaperEnabled is whether the storage reaper runs
// here: mode auto follows it. Nil-safe.
func (r *Reclaimer) Run(ctx context.Context, reaperEnabled bool) {
	if r == nil {
		return
	}
	run, dryRun := r.Config.Effective(reaperEnabled)
	if !run {
		logging.InfoContext(ctx, "[idlereclaim] Idle reclaim off (RELIANT_IDLE_RECLAIM=auto runs it where the storage reaper runs; on or dry-run run it anywhere)",
			"mode", r.Config.Mode)
		return
	}
	logging.InfoContext(ctx, "[idlereclaim] Idle reclaim scheduled", "mode", r.Config.Mode, "dry_run", dryRun, "interval", PassInterval,
		"scratch_root", r.ScratchRoot, "scratch_strip_idle", r.Config.ScratchStripIdle, "scratch_remove_idle", r.Config.ScratchRemoveIdle,
		"worktree_root", r.WorktreeRoot, "worktree_node_modules_idle", r.Config.WorktreeNodeModulesIdle)
	timer := time.NewTimer(firstPassDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.Pass(ctx, dryRun, "scheduled")
		timer.Reset(PassInterval)
	}
}

// Pass runs one pass and returns its report, which it also writes to
// StateDir. dryRun decides and removes nothing. It never panics.
func (r *Reclaimer) Pass(ctx context.Context, dryRun bool, trigger string) (rep Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep = Report{
		Mode: r.Config.Mode, DryRun: dryRun, Trigger: trigger, StartedAt: r.now(),
		ScratchStripIdle: r.Config.ScratchStripIdle.String(), ScratchRemoveIdle: r.Config.ScratchRemoveIdle.String(),
		WorktreeNodeModulesIdle: r.Config.WorktreeNodeModulesIdle.String(),
		ScratchRoot:             r.ScratchRoot, WorktreeRoot: r.WorktreeRoot,
	}
	defer func() {
		if p := recover(); p != nil {
			logging.WarnContext(ctx, "[idlereclaim] Pass panicked", "panic", fmt.Sprint(p))
		}
		rep.FinishedAt = r.now()
		rep.total()
		if err := writeReport(r.StateDir, rep); err != nil {
			logging.WarnContext(ctx, "[idlereclaim] Could not write the report", "error", err)
		}
		logging.InfoContext(ctx, "[idlereclaim] Pass finished", "dry_run", dryRun, "trigger", trigger,
			"scratch_dirs", rep.Totals.ScratchDirs, "scratch_removed", rep.Totals.ScratchRemoved, "scratch_stripped", rep.Totals.ScratchStripped,
			"scratch_bytes", rep.Totals.ScratchBytes, "scratch_freed", HumanBytes(rep.Totals.ScratchBytes),
			"worktrees", rep.Totals.Worktrees, "worktrees_stripped", rep.Totals.WorktreesStripped,
			"worktree_bytes", rep.Totals.WorktreeBytes, "worktree_freed", HumanBytes(rep.Totals.WorktreeBytes),
			"duration", rep.FinishedAt.Sub(rep.StartedAt).Round(time.Second))
	}()
	if !dryRun {
		emptyTrash(ctx, r.ScratchRoot)
		emptyTrash(ctx, r.WorktreeRoot)
	}
	if r.ScratchRoot != "" {
		rep.Scratch = r.scratchPass(ctx, dryRun)
	}
	if r.WorktreeRoot != "" && ctx.Err() == nil {
		rep.Worktrees = r.worktreePass(ctx, dryRun)
	}
	if !dryRun {
		emptyTrash(ctx, r.ScratchRoot)
		emptyTrash(ctx, r.WorktreeRoot)
	}
	return rep
}

// logEntry logs what a pass did with one directory, when it did (or would
// do) something, or held one that was due for a reason a person may want to
// know. Every removal is a line.
func logEntry(ctx context.Context, kind string, e Entry) {
	attrs := []any{"kind", kind, "path", e.Path, "idle_hours", e.IdleHours, "bytes", e.Bytes, "freed", HumanBytes(e.Bytes), "removed", e.Removed}
	switch e.Action {
	case ActionRemoved, ActionStripped:
		if e.DryRun {
			logging.InfoContext(ctx, "[idlereclaim] would remove", attrs...)
		} else {
			logging.InfoContext(ctx, "[idlereclaim] removed", attrs...)
		}
		if len(e.Held) > 0 {
			logging.InfoContext(ctx, "[idlereclaim] kept part of it", "kind", kind, "path", e.Path, "held", e.Held)
		}
	case ActionHeld:
		logging.InfoContext(ctx, "[idlereclaim] held", "kind", kind, "path", e.Path, "idle_hours", e.IdleHours, "reason", e.Reason)
	}
}
