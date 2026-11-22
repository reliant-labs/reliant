// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
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
		e := r.scratchDir(ctx, filepath.Join(r.ScratchRoot, de.Name()), dryRun)
		logEntry(ctx, "scratch", e)
		out = append(out, e)
	}
	return out
}

// scratchDir decides, and unless dryRun does, what happens to one scratch
// directory.
func (r *Reclaimer) scratchDir(ctx context.Context, dir string, dryRun bool) Entry {
	now := r.now()
	s := scanDir(ctx, dir)
	used := latest(s.Newest, lastOf(r.ScratchUse, dir))
	idle := now.Sub(used)
	e := Entry{Path: dir, LastUse: used, IdleHours: int(idle.Hours()), DryRun: dryRun}
	switch {
	case s.Keep:
		e.Action, e.Reason = ActionKept, "it has a "+KeepFile+" file"
		return e
	case idle < r.Config.ScratchStripIdle:
		e.Action = ActionActive
		return e
	}
	if reason := s.unmeasured(); reason != "" {
		e.Action, e.Reason = ActionHeld, reason
		return e
	}
	if err := r.inUse(ctx, dir); err != nil {
		e.Action, e.Reason = ActionHeld, "in use: "+err.Error()
		return e
	}

	chatUsed := latest(used, lastOf(r.ChatUse, dir))
	switch {
	case now.Sub(chatUsed) >= r.Config.ScratchRemoveIdle:
		hold := ""
		for _, root := range s.GitRoots {
			if hold = uniqueWork(ctx, dir, root); hold != "" {
				break
			}
		}
		if hold == "" {
			return r.removeScratchDir(ctx, dir, s, e)
		}
		e.Held = append(e.Held, "the whole directory: "+hold)
	case idle >= r.Config.ScratchRemoveIdle:
		e.Held = append(e.Held, fmt.Sprintf("the whole directory: its chat made a tool call %s ago", now.Sub(chatUsed).Round(time.Hour)))
	}
	r.stripScratch(ctx, dir, s, &e, idle, dryRun)
	return e
}

// removeScratchDir removes the whole directory: moved into the trash under
// the admission lease, then the registrations of the linked worktrees that
// were in it are pruned from their repositories.
func (r *Reclaimer) removeScratchDir(ctx context.Context, dir string, s dirScan, e Entry) Entry {
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
	if _, err := r.moveAside(ctx, r.ScratchRoot, dir, dir); err != nil {
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
// worktrees of a directory idle for idle, recording what it removed.
func (r *Reclaimer) stripScratch(ctx context.Context, dir string, s dirScan, e *Entry, idle time.Duration, dryRun bool) {
	now := r.now()
	stripped := map[string]bool{}
	var removedAbs []string
	for _, t := range s.Targets {
		if ctx.Err() != nil {
			return
		}
		rel := relOf(dir, t)
		if gr := enclosingGitRoot(s.GitRoots, t); gr != nil && !gitIgnored(ctx, gr.Path, t) {
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
		if age := now.Sub(newest); age < r.Config.ScratchStripIdle {
			e.Held = append(e.Held, fmt.Sprintf("%s: changed %s ago", rel, age.Round(time.Minute)))
			continue
		}
		if !dryRun {
			if _, err := r.moveAside(ctx, r.ScratchRoot, dir, t); err != nil {
				e.Held = append(e.Held, rel+": "+err.Error())
				continue
			}
			removedAbs = append(removedAbs, t)
		}
		stripped[t] = true
		e.Bytes += n
		e.Removed = append(e.Removed, rel)
	}
	if !dryRun {
		r.Records.add(dir, now, idle, removedAbs)
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
		if reason := removableWorktree(ctx, root.Path, stripped); reason != "" {
			e.Held = append(e.Held, rel+": a git worktree kept because "+reason)
			continue
		}
		n := dirBytes(root.Path, stripped)
		if !dryRun {
			err := r.underLease(ctx, dir, func() error {
				return keepingMtime(filepath.Dir(root.Path), r.ScratchRoot, func() error { return removeWorktree(ctx, root.Path) })
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
