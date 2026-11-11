// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Reclaim in two phases (designs/worktree-reclaim-tombstone-2026-11-11.md).
// On 2026-11-10 a worktree an agent was still using was archived three hours
// after it looked quiet, and nothing told the agent. Now:
//
//  1. Mark. A live worktree whose work has landed (or that is complete,
//     clean and idle) and that has had no activity for the reclaim quiet
//     period is marked: its daemon places a tombstone in it, which anything
//     working there sees, and the row records the mark. Nothing is archived.
//  2. Reclaim. No sooner than the grace period after the mark, it is
//     archived (and so retired) only if the tombstone still carries the same
//     mark, nothing at all has happened in it since, and its work is still
//     landed or idle. Anything else cancels the mark and the quiet clock
//     starts again.
//
// Cancelling is the default: touching the worktree, deleting the tombstone,
// a chat or run in it, new work. Nothing has to be done to keep a worktree.
// A daemon that does not report tombstones (Result.Tombstone) has nothing
// marked or reclaimed: its rows wait, as "Keep everything" does.

const (
	// DefaultReclaimQuiet is how long a worktree must have been quiet before
	// it is marked, and DefaultReclaimGrace how long a mark stands before
	// the worktree is reclaimed. Both are also the floors: a shorter
	// configured value is raised to them, so a typo can never bring back a
	// three-hour reclaim.
	DefaultReclaimQuiet = 24 * time.Hour
	DefaultReclaimGrace = 24 * time.Hour

	markTimeoutMs int32 = 120_000
)

// MarkStore records reclaim marks.
type MarkStore interface {
	WorktreeRunActivities(ctx context.Context, ids []string) (map[string]core.WorktreeRunActivity, error)
	MarkWorktreeForReclaim(ctx context.Context, id, markID string, quietSince time.Time, use *time.Time, reason string) (bool, error)
	ClearWorktreeMark(ctx context.Context, id, markID string) (bool, error)
	ArchiveMarkedWorktree(ctx context.Context, id, markID string) (bool, error)
}

type reclaimMarks struct {
	store        MarkStore
	quiet, grace time.Duration
}

// WithReclaimMarks makes every automatic reclaim go through a mark: quiet and
// grace below their defaults are raised to them. It replaces the direct
// landed and idle archives (WithLandedIdle's period then only decides
// whether live rows are asked if they landed at all).
func (s *Sweeper) WithReclaimMarks(store MarkStore, quiet, grace time.Duration) *Sweeper {
	if store == nil {
		s.marks = nil
		return s
	}
	s.marks = &reclaimMarks{store: store, quiet: max(quiet, DefaultReclaimQuiet), grace: max(grace, DefaultReclaimGrace)}
	return s
}

// reclaimable is the archive reason a live answer offers the row for, or "".
func reclaimable(r worktreereclaim.Result) string {
	switch {
	case r.Landed:
		return core.ArchivedBecauseLanded
	case r.RecoverableIdle:
		return core.ArchivedBecauseRecoverableIdle
	}
	return ""
}

// advanceMark moves a live row through mark and reclaim on its daemon's
// answer r: marks a quiet candidate, cancels a mark anything has
// contradicted, and reclaims one whose grace has run out.
func (s *Sweeper) advanceMark(ctx context.Context, userID, daemonID string, wt *core.Worktree, projectPath string, r worktreereclaim.Result) {
	m := s.marks
	marked := wt.CleanupMetadata.Marked()
	reason := reclaimable(r)
	if r.Tombstone == nil {
		// A daemon without tombstones never has anything reclaimed. A mark
		// left from before a downgrade is cleared: its tombstone is unseen.
		if marked {
			s.cancelMark(ctx, userID, daemonID, wt, projectPath, "the machine no longer reports reclaim tombstones")
		}
		return
	}
	if !marked {
		if reason != "" && !wt.IsMain && (s.keep == nil || !s.keep.KeepFiles(ctx, userID)) {
			s.placeMark(ctx, userID, daemonID, wt, projectPath, reason)
		}
		return
	}
	meta := wt.CleanupMetadata
	if why := s.markContradicted(ctx, wt, meta, r); why != "" {
		s.cancelMark(ctx, userID, daemonID, wt, projectPath, why)
		return
	}
	if s.now().Before(meta.ReclaimMarkedAt.Add(m.grace)) {
		return
	}
	if s.auto == nil || s.auto.mode != AutoArchiveOn {
		logging.InfoContext(ctx, "[WorktreeSweep] would reclaim marked worktree", "worktreeID", wt.ID, "name", wt.Name, "path", wt.Path, "markID", meta.ReclaimMarkID)
		return
	}
	archived, err := m.store.ArchiveMarkedWorktree(ctx, wt.ID, meta.ReclaimMarkID)
	if err != nil {
		s.archiveErrors.Add(1)
		logging.WarnContext(ctx, "[WorktreeSweep] could not reclaim marked worktree", "worktreeID", wt.ID, "error", err)
		return
	}
	if !archived {
		// Something moved under the row lock (a run started, a chat bound,
		// the mark was cleared): the next answer decides again.
		logging.InfoContext(ctx, "[WorktreeSweep] marked worktree not reclaimed: activity under the row lock", "worktreeID", wt.ID)
		return
	}
	s.landedArchived.Add(1)
	logging.InfoContext(ctx, "[WorktreeSweep] reclaimed marked worktree", "worktreeID", wt.ID, "name", wt.Name, "markID", meta.ReclaimMarkID,
		"markedAt", meta.ReclaimMarkedAt, "reason", meta.ReclaimMarkReason)
	if s.auto.notify != nil {
		s.auto.notify(ctx, userID, wt.ProjectID, wt.ID)
	}
}

// markContradicted says why a standing mark no longer holds, or "".
func (s *Sweeper) markContradicted(ctx context.Context, wt *core.Worktree, meta *core.CleanupMetadata, r worktreereclaim.Result) string {
	switch {
	case r.Tombstone.MarkID != meta.ReclaimMarkID:
		return "its reclaim tombstone was removed"
	case reclaimable(r) == "":
		return "it has work that has not landed"
	case meta.ReclaimMarkedAt == nil:
		return "the mark has no time"
	case r.LastUse != nil && (meta.ReclaimMarkUse == nil || r.LastUse.After(*meta.ReclaimMarkUse)):
		// Both stamps are the daemon's, so its clock never matters here.
		return "it was used after it was marked"
	}
	acts, err := s.marks.store.WorktreeRunActivities(ctx, []string{wt.ID})
	if err != nil {
		// Unknown is not quiet: keep the mark, decide nothing this pass.
		return ""
	}
	if a := acts[wt.ID]; a.Running || a.LastActive.After(*meta.ReclaimMarkedAt) {
		return "a chat or run was active in it after it was marked"
	}
	return ""
}

// placeMark marks a live candidate whose rows have been quiet for the quiet
// period: the daemon places the tombstone, then the row records the mark
// under its lock. A mark the row refuses (activity raced it) has its
// tombstone removed again.
func (s *Sweeper) placeMark(ctx context.Context, userID, daemonID string, wt *core.Worktree, projectPath, reason string) {
	m := s.marks
	quietSince := s.now().Add(-m.quiet)
	acts, err := m.store.WorktreeRunActivities(ctx, []string{wt.ID})
	if err != nil {
		return
	}
	if a := acts[wt.ID]; a.Running || a.LastActive.After(quietSince) {
		return
	}
	w, ok := s.describeOne(ctx, wt, projectPath)
	if !ok {
		return
	}
	mark := worktreereclaim.ReclaimMark{ID: uuid.NewString(), ReclaimAt: s.now().Add(m.grace).UTC()}
	// Probed as a reconcile probes it, so the use stamp in the answer (the
	// baseline) is measured the way every later one is.
	w.Mark, w.ProbeLanded = &mark, s.reclaimsLanded()
	resp, err := s.send(ctx, target{userID, daemonID}, worktreereclaim.MarkCommand, markTimeoutMs, worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}})
	if err != nil || len(resp.Results) != 1 {
		logging.WarnContext(ctx, "[WorktreeSweep] could not mark worktree for reclaim", "worktreeID", wt.ID, "error", err)
		return
	}
	res := resp.Results[0]
	if res.Outcome != worktreereclaim.OutcomeLocked || res.Tombstone == nil || res.Tombstone.MarkID != mark.ID {
		logging.WarnContext(ctx, "[WorktreeSweep] daemon did not place the reclaim tombstone", "worktreeID", wt.ID,
			"outcome", res.Outcome, "error", res.Error)
		s.removeTombstone(ctx, userID, daemonID, wt, projectPath, mark.ID)
		return
	}
	// The tombstone's own writes are in this stamp, so it is the baseline
	// later stamps are compared with, and is never recorded as use (noteUse
	// skips it while the mark stands).
	ok, err = m.store.MarkWorktreeForReclaim(ctx, wt.ID, mark.ID, quietSince, res.LastUse, reason)
	if err != nil || !ok {
		logging.InfoContext(ctx, "[WorktreeSweep] worktree not marked: activity under the row lock", "worktreeID", wt.ID, "error", err)
		s.removeTombstone(ctx, userID, daemonID, wt, projectPath, mark.ID)
		return
	}
	logging.InfoContext(ctx, "[WorktreeSweep] marked worktree for reclaim", "worktreeID", wt.ID, "name", wt.Name, "path", wt.Path,
		"markID", mark.ID, "reclaimAt", mark.ReclaimAt, "reason", reason, "mode", s.modeName())
}

// cancelMark clears the row's mark and removes its tombstone.
func (s *Sweeper) cancelMark(ctx context.Context, userID, daemonID string, wt *core.Worktree, projectPath, why string) {
	id := wt.CleanupMetadata.ReclaimMarkID
	if _, err := s.marks.store.ClearWorktreeMark(ctx, wt.ID, id); err != nil {
		logging.WarnContext(ctx, "[WorktreeSweep] could not cancel reclaim mark", "worktreeID", wt.ID, "error", err)
		return
	}
	logging.InfoContext(ctx, "[WorktreeSweep] reclaim mark cancelled", "worktreeID", wt.ID, "name", wt.Name, "markID", id, "why", why)
	s.removeTombstone(ctx, userID, daemonID, wt, projectPath, id)
}

// removeTombstone asks the daemon to remove mark id's tombstone. Failing is
// harmless: a tombstone with no mark on the row authorizes nothing, and the
// next mark replaces it.
func (s *Sweeper) removeTombstone(ctx context.Context, userID, daemonID string, wt *core.Worktree, projectPath, id string) {
	w, ok := s.describeOne(ctx, wt, projectPath)
	if !ok {
		return
	}
	w.Unmark = id
	if _, err := s.send(ctx, target{userID, daemonID}, worktreereclaim.UnmarkCommand, markTimeoutMs, worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}); err != nil {
		logging.WarnContext(ctx, "[WorktreeSweep] could not remove reclaim tombstone", "worktreeID", wt.ID, "error", err)
	}
}

// describeOne is the live row's description as a reconcile sends it, locked
// only: no probe, shed or retire.
func (s *Sweeper) describeOne(ctx context.Context, wt *core.Worktree, projectPath string) (worktreereclaim.Worktree, bool) {
	req, _ := s.describe(ctx, []*core.ReclaimCandidate{{Worktree: wt, ProjectPath: projectPath}}, nil, true, map[string]bool{wt.ID: true})
	if len(req.Worktrees) != 1 || req.Worktrees[0].State != worktreereclaim.StateActive || req.Worktrees[0].Retire != "" {
		return worktreereclaim.Worktree{}, false
	}
	w := req.Worktrees[0]
	w.ProbeIdle, w.ProbeLanded, w.ShedAfterSeconds, w.ProbeRecoverableAfterSeconds, w.KeepFiles = false, false, 0, 0, false
	return w, true
}

func (s *Sweeper) modeName() string {
	if s.auto != nil && s.auto.mode == AutoArchiveOn {
		return "on"
	}
	return "dry-run"
}
