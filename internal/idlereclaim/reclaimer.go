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
	// Pressure reports whether a volume holding the scratch or worktree root
	// is under pressure (VolumePressure); Run then runs pressure passes. Nil:
	// never.
	Pressure func() bool
}

// Reclaimer runs the idle reclaim over one scratch root and one worktree root.
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
	// BoundaryCheckEvery paces the lease and process checks at deletion
	// boundaries (worktreereclaim.Quarantine.CheckEvery); 0 checks before
	// every entry. NewReclaimer sets worktreereclaim.DefaultDisposalCheckEvery.
	BoundaryCheckEvery time.Duration
	Deps

	mu sync.Mutex // one pass at a time
	// pass is the running pass's floors (Config, or its pressure floors);
	// nil outside a pass. Guarded by mu.
	pass *Config
}

// floors is the configuration the running pass applies.
func (r *Reclaimer) floors() Config {
	if r.pass != nil {
		return *r.pass
	}
	return r.Config
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
		Records:            NewRecords(filepath.Join(state, RecordFile)),
		BoundaryCheckEvery: worktreereclaim.DefaultDisposalCheckEvery,
		Deps: Deps{
			ProcessUse:  worktreereclaim.CheckProcessUse,
			Inspect:     worktreereclaim.InspectRegenerable,
			NodeModules: worktreereclaim.NodeModulesTargets,
			Pressure:    VolumePressure(scratch, worktrees),
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

// inUse reports a live process using any of paths.
func (r *Reclaimer) inUse(ctx context.Context, paths ...string) error {
	if r.ProcessUse == nil {
		return fmt.Errorf("process inspection unavailable")
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.ProcessUse(probe, paths)
}

// Run runs a pass every PassInterval until ctx is done, when the mode says
// passes run on this daemon, and a pressure pass (TriggerPressure) within
// minutes of a volume coming under pressure, then every PressurePassInterval
// while it stays. reaperEnabled is whether the storage reaper runs here: mode
// auto follows it. Nil-safe.
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
		"worktree_root", r.WorktreeRoot, "worktree_node_modules_idle", r.Config.WorktreeNodeModulesIdle,
		"pressure_interval", PressurePassInterval, "pressure_strip_idle", PressureStripIdle)
	started := r.now()
	var last time.Time
	ticker := time.NewTicker(pressureCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pressure := r.Pressure != nil && r.Pressure()
		trigger, due := nextPass(pressure, started, last, r.now())
		if !due {
			continue
		}
		last = r.now()
		r.Pass(ctx, dryRun, trigger)
	}
}

// Pass runs one pass and returns its report, which it also writes to
// StateDir. dryRun decides and removes nothing. trigger TriggerPressure
// applies the pressure floors. It never panics.
func (r *Reclaimer) Pass(ctx context.Context, dryRun bool, trigger string) (rep Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := r.Config
	if trigger == TriggerPressure {
		cfg = cfg.pressureFloors()
	}
	r.pass = &cfg
	defer func() { r.pass = nil }()
	rep = Report{
		Mode: r.Config.Mode, DryRun: dryRun, Trigger: trigger, StartedAt: r.now(),
		ScratchStripIdle: cfg.ScratchStripIdle.String(), ScratchRemoveIdle: cfg.ScratchRemoveIdle.String(),
		WorktreeNodeModulesIdle: cfg.WorktreeNodeModulesIdle.String(),
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
	var parkedScratch, parkedWorktrees []Entry
	if !dryRun {
		parkedScratch, parkedWorktrees = r.recoverTrash(ctx, r.ScratchRoot), r.recoverTrash(ctx, r.WorktreeRoot)
	}
	if r.ScratchRoot != "" {
		rep.Scratch = r.scratchPass(ctx, dryRun)
	}
	if r.WorktreeRoot != "" && ctx.Err() == nil {
		rep.Worktrees = r.worktreePass(ctx, dryRun)
	}
	if !dryRun {
		// A tree this pass could not put back is reported once, by the
		// recovery that runs at the end; the next pass tries again.
		parkedScratch, parkedWorktrees = r.recoverTrash(ctx, r.ScratchRoot), r.recoverTrash(ctx, r.WorktreeRoot)
	}
	rep.Scratch = append(rep.Scratch, parkedScratch...)
	rep.Worktrees = append(rep.Worktrees, parkedWorktrees...)
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
