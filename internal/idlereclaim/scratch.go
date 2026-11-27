// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The scratch lifecycle. A scratch directory's last use is the latest of:
//
//   - the newest modification time of anything in it (scanDir);
//   - the last time daemon work named a path in it (Deps.ScratchUse): a file
//     read or a command's working directory leaves no mtime behind, and a
//     briefing is mostly read.
//
// After ScratchStripIdle without use, and with no process using it, its
// regenerable output goes (a node_modules anywhere, a build output beside
// its manifest, each git-ignored when it is inside a git working tree and
// each passing the regenerable inventory), and then each linked git worktree
// in it that is clean, unlocked, pushed and holds nothing else git ignores.
//
// After ScratchRemoveIdle the whole directory goes, but that clock also runs
// from the last tool call of the chat it is named for (Deps.ChatUse): a chat
// that is still working may come back for a briefing it wrote a week ago,
// and only the daemon's own record of the chat's calls says it is alive (the
// daemon cannot see whether the chat is archived; an archived chat makes no
// calls, so its directory goes on the files' clock). And never while a git
// working tree in it holds work that exists nowhere else (uniqueWork): then
// only the strip runs, and the report names what holds it.
//
// A KeepFile at the top keeps the directory and everything in it.

func (r *Reclaimer) scratchPass(ctx context.Context, dryRun bool) []Entry {
	entries, err := os.ReadDir(r.ScratchRoot)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, de := range entries {
		if ctx.Err() != nil {
			break
		}
		if strings.HasPrefix(de.Name(), ".") || !de.IsDir() {
			continue // the trash, and anything that is not a chat's directory
		}
		for _, e := range r.scratchDir(ctx, filepath.Join(r.ScratchRoot, de.Name()), dryRun, true) {
			logEntry(ctx, "scratch", e)
			out = append(out, e)
		}
	}
	return out
}

// agentDirName matches a sub-agent's directory in its chat's scratch
// directory: the system prompt gives each sub-agent <chat dir>/<agent id>/,
// and an agent id is a UUID.
var agentDirName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// scratchDir decides, and unless dryRun does, what happens to one scratch
// directory, and returns its entry (and, for a chat's directory, those of its
// sub-agents' directories first). Only a directory idle as a whole is
// admitted as one (its lease covers it all); the units of any other are each
// admitted on their own, so a pass never takes the lease over a chat's
// directory while the chat works in it.
//
// A chat's directory that is not idle as a whole is one directory for the
// chat and every sub-agent it spawns: a chat that orchestrates for weeks
// keeps it in use, while each finished sub-agent's directory, with the clones
// and worktrees it made, sits idle. Each sub-agent's directory therefore has
// the lifecycle of a scratch directory of its own, on its own clock (its
// files and the recorded use of a path in it; the chat's tool calls are the
// chat's, not the sub-agent's).
func (r *Reclaimer) scratchDir(ctx context.Context, dir string, dryRun, chat bool) []Entry {
	e, s, whole := r.scratchVerdict(ctx, dir)
	if !whole {
		if e.Action == ActionKept {
			return []Entry{e}
		}
		var out []Entry
		if chat {
			children := agentDirs(dir)
			for _, child := range children {
				if ctx.Err() != nil {
					break
				}
				out = append(out, r.scratchDir(ctx, child, dryRun, false)...)
			}
			// Each sub-agent's directory was handled whole by its own entry.
			s = s.without(children)
		}
		r.stripUnits(ctx, dir, s, &e, dryRun)
		return append(out, e)
	}
	return []Entry{r.scratchDirWhole(ctx, dir, s, e, dryRun, chat)}
}

// agentDirs lists the sub-agents' directories in the chat directory dir.
func agentDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range entries {
		if de.IsDir() && agentDirName.MatchString(de.Name()) {
			out = append(out, filepath.Join(dir, de.Name()))
		}
	}
	return out
}

// scratchDirWhole handles a directory idle as a whole: under its admission
// lease unless dryRun.
func (r *Reclaimer) scratchDirWhole(ctx context.Context, dir string, s dirScan, e Entry, dryRun, chat bool) Entry {
	if dryRun {
		return r.scratchWhole(ctx, dir, s, e, true, chat)
	}
	err := r.underLease(ctx, dir, r.ScratchRoot, func(ctx context.Context) error {
		e = r.scratchDirAdmitted(ctx, dir, chat)
		return nil
	})
	if err != nil {
		e = Entry{Path: dir, LastUse: e.LastUse, IdleHours: e.IdleHours, Action: ActionHeld, Reason: err.Error()}
		r.stripUnits(ctx, dir, s, &e, false)
	}
	return e
}

// scratchVerdict reads whether the directory may be handled as a whole: kept
// (a KeepFile), not idle, not measurable, or in use say no, and the entry
// says why.
func (r *Reclaimer) scratchVerdict(ctx context.Context, dir string) (Entry, dirScan, bool) {
	now := r.now()
	s := scanDir(ctx, dir)
	used := latest(s.Newest, lastOf(r.ScratchUse, dir))
	idle := now.Sub(used)
	e := Entry{Path: dir, LastUse: used, IdleHours: int(idle.Hours())}
	switch {
	case s.Keep:
		e.Action, e.Reason = ActionKept, "it has a "+KeepFile+" file"
		return e, s, false
	case idle < r.floors().ScratchStripIdle:
		// A chat's directory is one directory for the chat and every
		// sub-agent it spawns, so a long-running chat keeps it in use for
		// weeks while the checkouts its finished agents left in it sit
		// idle: each of those is stripped on its own clock (stripUnits).
		e.Action = ActionActive
		return e, s, false
	}
	if reason := s.unmeasured(); reason != "" {
		e.Action, e.Reason = ActionHeld, reason
		return e, s, false
	}
	if err := r.inUse(ctx, dir); err != nil {
		e.Action, e.Reason = ActionHeld, "in use: "+err.Error()
		return e, s, false
	}
	return e, s, true
}

// All authorization, including activity and git state, is read after
// admission. A directory found no longer idle as a whole is left to the next
// pass.
func (r *Reclaimer) scratchDirAdmitted(ctx context.Context, dir string, chat bool) Entry {
	e, s, whole := r.scratchVerdict(ctx, dir)
	if !whole {
		return e
	}
	return r.scratchWhole(ctx, dir, s, e, false, chat)
}

// scratchWhole removes, or strips, a directory idle as a whole.
func (r *Reclaimer) scratchWhole(ctx context.Context, dir string, s dirScan, e Entry, dryRun, chat bool) Entry {
	now := r.now()
	e.DryRun = dryRun
	idle := now.Sub(e.LastUse)
	chatUsed := e.LastUse
	if chat {
		chatUsed = latest(chatUsed, lastOf(r.ChatUse, dir))
	}
	switch {
	case now.Sub(chatUsed) >= r.floors().ScratchRemoveIdle:
		hold := wholeRemovalHold(ctx, dir, s)
		if hold == "" {
			return r.removeScratchDir(ctx, dir, s, e, chat)
		}
		e.Held = append(e.Held, "the whole directory: "+hold)
	case idle >= r.floors().ScratchRemoveIdle:
		e.Held = append(e.Held, fmt.Sprintf("the whole directory: its chat made a tool call %s ago", now.Sub(chatUsed).Round(time.Hour)))
	}
	r.stripScratch(ctx, dir, s, &e, idle, dryRun, nil)
	return e
}

// stripUnits strips a scratch directory that is not idle as a whole, or that
// a pass cannot vouch for as a whole (unmeasured, a process in it, its lease
// refused), unit by unit: a unit is the git working tree a target is in, or
// the directory holding its manifest, and is stripped once IT has gone unused
// for the strip floor (its newest file, its git HEAD and reflog, recorded use
// of a path in it) and no process uses it. Each removal runs under the
// unit's own admission lease. The entry keeps its action and reason when
// nothing goes.
func (r *Reclaimer) stripUnits(ctx context.Context, dir string, s dirScan, e *Entry, dryRun bool) {
	e.DryRun = dryRun
	action, reason := e.Action, e.Reason
	r.stripScratch(ctx, dir, s, e, 0, dryRun, r.unitGate(dir))
	if len(e.Removed) == 0 {
		e.Action, e.Reason = action, reason
	}
}

// unitCheck is the gate's answer for one unit of a scratch directory.
type unitCheck struct {
	ok   bool
	hold string // why a unit idle long enough is held; "" for an active one
	idle time.Duration
}

// unitGate decides, once per unit, whether a unit of the scratch directory
// dir is idle and unused. The directory itself is never a unit here: the
// caller already found it in use.
func (r *Reclaimer) unitGate(dir string) func(ctx context.Context, unit string) unitCheck {
	seen := map[string]bool{}
	return func(ctx context.Context, unit string) unitCheck {
		if !within(dir, unit) || filepath.Clean(unit) == filepath.Clean(dir) {
			return unitCheck{}
		}
		c := r.checkUnit(ctx, dir, unit, true)
		if seen[unit] {
			c.hold = "" // reported once
		}
		seen[unit] = true
		return c
	}
}

// checkUnit is one unit's activity, and with process its process use.
func (r *Reclaimer) checkUnit(ctx context.Context, dir, unit string, process bool) unitCheck {
	rel := relOf(dir, unit)
	newest, why := checkoutNewest(ctx, unit)
	if why != "" {
		return unitCheck{hold: rel + ": " + why}
	}
	idle := r.now().Sub(latest(newest, shallowActivity(unit), lastOf(r.ScratchUse, unit)))
	if idle < r.floors().ScratchStripIdle {
		return unitCheck{idle: idle}
	}
	if process {
		if err := r.inUse(ctx, unit); err != nil {
			return unitCheck{hold: rel + ": in use: " + err.Error(), idle: idle}
		}
	}
	return unitCheck{ok: true, idle: idle}
}

// unitOf is the unit a target of a scratch directory belongs to: its git
// working tree, or the directory holding its manifest.
func unitOf(t string, gr *gitRoot) string {
	if gr != nil {
		return gr.Path
	}
	return filepath.Dir(t)
}

// gitRootAbove is the innermost git working tree, at dir or below it, that p
// is inside, found from p upwards rather than from a scan that may have
// stopped before reaching its .git; nil when there is none.
func gitRootAbove(dir, p string) *gitRoot {
	dir = filepath.Clean(dir)
	for cur := filepath.Dir(filepath.Clean(p)); within(dir, cur); cur = filepath.Dir(cur) {
		if info, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return &gitRoot{Path: cur, Linked: info.Mode().IsRegular()}
		}
		if cur == dir {
			break
		}
	}
	return nil
}

// wholeRemovalHold is why the scratch directory dir may not go whole, or "":
// a git checkout in it holding work only it has. Such work is never captured
// and deleted here: a capture is a local ref, not a durable receipt. A linked
// worktree's is proposed for approval by the machine-wide worktree reclaim,
// which discovers every linked worktree, scratch ones included.
func wholeRemovalHold(ctx context.Context, dir string, s dirScan) string {
	for _, root := range s.GitRoots {
		if hold := uniqueWork(ctx, dir, root); hold != "" {
			return hold
		}
	}
	return ""
}

// removeScratchDir removes only the descriptor-inventoried tree under
// admission, then prunes its linked-worktree registrations. An incomplete
// inventory or changed identity/content holds rather than falling back to trash.
func (r *Reclaimer) removeScratchDir(ctx context.Context, dir string, s dirScan, e Entry, chat bool) Entry {
	e.Bytes = dirBytes(dir, nil)
	e.Removed = []string{"."}
	e.Action = ActionRemoved
	if e.DryRun {
		return e
	}
	var commons []string
	for _, root := range s.GitRoots {
		if root.Linked {
			if c := commonDir(root.Path); c != "" && !within(dir, c) {
				commons = append(commons, c)
			}
		}
	}
	// The content checks run again here, under the admission, on the tree as
	// inventoried; the process and lease checks run once it is parked.
	if err := worktreereclaim.RemoveInventoriedScratch(ctx, dir, r.quarantine(ctx, dir, func(ctx context.Context) error {
		fresh := scanDir(ctx, dir)
		used := latest(fresh.Newest, lastOf(r.ScratchUse, dir))
		if chat {
			used = latest(used, lastOf(r.ChatUse, dir))
		}
		if fresh.Keep || fresh.unmeasured() != "" || r.now().Sub(used) < r.floors().ScratchRemoveIdle {
			return fmt.Errorf("scratch authorization changed")
		}
		if reason := wholeRemovalHold(ctx, dir, fresh); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return nil
	})); err != nil {
		e.Action, e.Reason, e.Bytes, e.Removed = ActionHeld, "removing it: "+err.Error(), 0, nil
		return e
	}
	for _, c := range commons {
		pruneWorktrees(ctx, c)
	}
	r.Records.forget(dir)
	return e
}

// stripScratch removes the regenerable output and the clean, pushed linked
// worktrees of a directory idle for idle, recording what it removed. With a
// gate, only what is in a unit the gate passes (stripUnits); the removal is
// then recorded per unit, with the unit's idle.
func (r *Reclaimer) stripScratch(ctx context.Context, dir string, s dirScan, e *Entry, idle time.Duration, dryRun bool, gate func(context.Context, string) unitCheck) {
	now := r.now()
	floor := r.floors().ScratchStripIdle
	stripped := map[string]bool{}
	removed := map[string][]string{} // record root -> removed paths
	idleOf := map[string]time.Duration{dir: idle}
	for _, t := range s.Targets {
		if ctx.Err() != nil {
			return
		}
		rel := relOf(dir, t)
		gr := gitRootAbove(dir, t)
		record := dir
		if gate != nil {
			unit := unitOf(t, gr)
			c := gate(ctx, unit)
			if !c.ok {
				if c.hold != "" {
					e.Held = append(e.Held, c.hold)
				}
				continue
			}
			record, idleOf[unit] = unit, c.idle
		}
		if gr != nil && !untrackedOutput(ctx, gr.Path, t) {
			e.Held = append(e.Held, rel+": git does not ignore it")
			continue
		}
		if r.Inspect == nil {
			e.Held = append(e.Held, rel+": no inventory available")
			continue
		}
		n, newest, err := r.Inspect(ctx, t)
		if err != nil {
			e.Held = append(e.Held, rel+": "+err.Error())
			continue
		}
		if age := now.Sub(newest); age < floor {
			e.Held = append(e.Held, fmt.Sprintf("%s: changed %s ago", rel, age.Round(time.Minute)))
			continue
		}
		if !dryRun {
			// record is the guard: the directory, under the admission the
			// caller holds, or the unit, under one of its own.
			if err := r.underLease(ctx, record, r.ScratchRoot, func(ctx context.Context) error {
				return keepingMtime(filepath.Dir(t), r.ScratchRoot, func() error {
					return worktreereclaim.RemoveInventoriedOutput(ctx, t, floor, now, r.quarantine(ctx, record, func(ctx context.Context) error {
						if gr := gitRootAbove(dir, t); gr != nil && !untrackedOutput(ctx, gr.Path, t) {
							return fmt.Errorf("output contains tracked files or is not ignored")
						}
						if gate != nil && !r.checkUnit(ctx, dir, record, false).ok {
							return fmt.Errorf("its checkout was used since it was checked")
						}
						return nil
					}))
				})
			}); err != nil {
				e.Held = append(e.Held, rel+": "+err.Error())
				continue
			}
			removed[record] = append(removed[record], t)
		}
		stripped[t] = true
		e.Bytes += n
		e.Removed = append(e.Removed, rel)
	}
	for root, paths := range removed {
		r.Records.add(root, now, idleOf[root], paths)
	}

	if r.WorktreeSweepOn {
		// The machine-wide worktree reclaim owns every linked worktree's
		// removal: it captures each into a ref first.
		r.finishStrip(e)
		return
	}
	for _, root := range s.GitRoots {
		if ctx.Err() != nil {
			return
		}
		if !root.Linked || enclosedByTarget(stripped, root.Path) {
			continue
		}
		rel := relOf(dir, root.Path)
		if gate != nil {
			if c := gate(ctx, root.Path); !c.ok {
				if c.hold != "" {
					e.Held = append(e.Held, c.hold)
				}
				continue
			}
		}
		if reason := removableWorktree(ctx, root.Path, stripped); reason != "" {
			e.Held = append(e.Held, rel+": a git worktree kept because "+reason)
			continue
		}
		n := dirBytes(root.Path, stripped)
		if !dryRun {
			guard := dir
			if gate != nil {
				guard = root.Path
			}
			err := r.underLease(ctx, guard, r.ScratchRoot, func(ctx context.Context) error {
				return keepingMtime(filepath.Dir(root.Path), r.ScratchRoot, func() error { return removeWorktree(ctx, root.Path, r.quarantine(ctx, guard, nil)) })
			})
			if err != nil {
				e.Held = append(e.Held, rel+": removing the git worktree: "+err.Error())
				continue
			}
		}
		e.Bytes += n
		e.Removed = append(e.Removed, rel+" (git worktree)")
	}
	r.finishStrip(e)
}

func (r *Reclaimer) finishStrip(e *Entry) {
	switch {
	case len(e.Removed) > 0:
		e.Action = ActionStripped
	case len(e.Held) > 0:
		e.Action, e.Reason = ActionHeld, e.Held[0]
	default:
		e.Action = ActionNothing
	}
}

// enclosedByTarget reports whether p is inside one of the stripped targets.
func enclosedByTarget(stripped map[string]bool, p string) bool {
	for t := range stripped {
		if within(t, p) {
			return true
		}
	}
	return false
}

// dirBytes is the apparent size of the regular files under dir, not
// following symlinks and not counting what is under a path in skip.
func dirBytes(dir string, skip map[string]bool) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skip[p] {
			return fs.SkipDir
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
