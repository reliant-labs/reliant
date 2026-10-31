// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxStripWalk bounds how many ignored entries one checkout's target
	// search visits, and how many children one directory may have in an
	// inventory. It only ever stops early.
	maxStripWalk = 50_000
	// maxStrippedNames bounds the paths a result names; the bytes count all.
	maxStrippedNames = 20
	// shedMinBudget is the request time that must remain to start shedding:
	// deleting a large node_modules takes a while, and a result the deadline
	// cuts off is deferred, so what was shed would go unrecorded.
	shedMinBudget = 30 * time.Second
)

// shedsFor reports whether an archived worktree held for this reason gets its
// regenerable output shed. These holds keep the directory for the work in it,
// which shedding never touches. Every other hold is about whose the directory
// is, or whether it can be inspected at all (unmanaged, unverified, in use,
// quarantined), or the user chose to keep it (kept): no file in it is touched.
func shedsFor(reason HeldReason) bool {
	switch reason {
	case ReasonDirty, ReasonUnpushed, ReasonUnreachable, ReasonData, ReasonOutsideFiles, ReasonNestedRepo:
		return true
	}
	return false
}

// shedRegenerable deletes the regenerable output of a worktree that is kept
// for its work, and records it on res: the paths git ignores AND the
// allow-list says an install, build or tool recreates (rebuildablePath:
// node_modules, .next, dist, build and tool caches, the checkout's tmp/).
// Tracked files, untracked files git does not ignore, and ignored files off
// the allow-list are never touched. The worktree keeps working; its install
// or build step puts the output back.
//
// The server asks for it (Worktree.ShedAfterSeconds) for a live row no chat
// is running in, and for an archived row held for its work. It runs only when
// every checkout is this row's (its lock says so), nothing in the worktree
// has changed for quiet, and no process uses it; and each target is removed
// through the retirement inventory (inspectRegenerableTree): descriptor-
// anchored, never following a symlink or crossing a mount, holding on a
// protected name (.env, *.sqlite, data, secrets), a nested repository or any
// change since it was inspected.
func (r *Reclaimer) shedRegenerable(ctx context.Context, w Worktree, quiet time.Duration, res *Result) {
	if res == nil || quiet <= 0 || w.KeepFiles || w.Remove || w.Retire != "" || r.ProcessUse == nil {
		return
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < shedMinBudget {
		return // the next pass sheds it
	}
	checkouts := ownedCheckouts(ctx, w)
	if len(checkouts) == 0 {
		return
	}
	last, err := idleSince(ctx, w.Path, checkouts)
	if err != nil || r.now().Sub(last) < quiet {
		return
	}
	if r.inUse(ctx, w, true) != nil || r.ProcessUse(ctx, []string{w.Path}) != nil {
		return
	}
	for _, c := range checkouts {
		for _, rel := range regenerableTargets(ctx, c) {
			if ctx.Err() != nil {
				return
			}
			full := filepath.Join(c, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
			freed, ok := r.shedTree(ctx, full, quiet)
			if !ok {
				continue
			}
			res.StrippedBytes += freed
			if len(res.Stripped) < maxStrippedNames {
				res.Stripped = append(res.Stripped, strings.TrimPrefix(relTo(w.Path, c)+"/"+strings.TrimSuffix(rel, "/"), "/"))
			}
		}
	}
	if res.StrippedBytes > 0 || len(res.Stripped) > 0 {
		r.mu.Lock()
		delete(r.sizes, w.Path) // the cached size is from before
		r.mu.Unlock()
	}
}

// shedHeld sheds an archived worktree that is held for the work in it
// (shedsFor), when the server asked for it.
func (r *Reclaimer) shedHeld(ctx context.Context, w Worktree, hold *Result) {
	if hold == nil || hold.Outcome != OutcomeHeld || !shedsFor(hold.Reason) || w.State != StateArchived {
		return
	}
	r.shedRegenerable(ctx, w, time.Duration(w.ShedAfterSeconds)*time.Second, hold)
}

// shedTree removes one regenerable directory (or file) if nothing in it has
// changed for quiet and no process uses it, and returns the bytes it held.
func (r *Reclaimer) shedTree(ctx context.Context, full string, quiet time.Duration) (int64, bool) {
	info, err := os.Lstat(full)
	if err != nil {
		return 0, false
	}
	if !info.IsDir() {
		// A single regenerable file (a *.tsbuildinfo): too small to be worth
		// a descriptor-anchored removal of its own.
		return 0, false
	}
	tree, err := inspectRegenerableTree(ctx, full)
	if err != nil {
		return 0, false
	}
	defer tree.close()
	if r.now().Sub(tree.newest) < quiet {
		return 0, false
	}
	if r.ProcessUse(ctx, []string{full}) != nil {
		return 0, false
	}
	if err := tree.remove(ctx, func() error { return ctx.Err() }); err != nil {
		return 0, false
	}
	return tree.bytes, true
}

// ownedCheckouts is the described checkouts of w that exist, are registered
// linked worktrees, and carry this row's reliant lock (active, archived or
// restored). Shedding touches nothing it cannot prove is the row's own.
func ownedCheckouts(ctx context.Context, w Worktree) []string {
	var out []string
	for _, c := range shallowCheckouts(w.Path, w.Checkouts) {
		if !isLinkedWorktree(c) {
			continue
		}
		info, err := inspectLock(ctx, c)
		if err != nil || !info.Registered || !info.Locked {
			continue
		}
		if p, ok := parseLock(info.Reason); ok && p.ID == w.ID {
			out = append(out, c)
		}
	}
	return out
}

// regenerableTargets lists the paths under checkout, relative to it, that git
// ignores and rebuildablePath accepts, outermost first. A nested repository
// inside one holds that target (the inventory refuses a .git anywhere in it).
func regenerableTargets(ctx context.Context, checkout string) []string {
	out, err := git(ctx, checkout, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil
	}
	var targets []string
	budget := maxStripWalk
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			collectRegenerable(checkout, e, &targets, &budget)
		}
	}
	return targets
}

// collectRegenerable walks one ignored path the way dataWalk.visit does, but
// collects the regenerable ones: a regenerable path is taken whole, and any
// other directory is judged by its contents.
func collectRegenerable(checkout, rel string, out *[]string, budget *int) {
	if *budget <= 0 {
		return
	}
	*budget--
	if rebuildablePath(rel) {
		*out = append(*out, rel)
		return
	}
	full := filepath.Join(checkout, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.IsDir() {
		return
	}
	children, err := os.ReadDir(full)
	if err != nil {
		return
	}
	for _, child := range children {
		name := strings.TrimSuffix(rel, "/") + "/" + child.Name()
		if child.IsDir() {
			name += "/"
		}
		collectRegenerable(checkout, name, out, budget)
	}
}
