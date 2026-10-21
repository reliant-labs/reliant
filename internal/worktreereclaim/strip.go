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
	// maxStripWalk bounds how many ignored entries one checkout's strip
	// visits looking for regenerable paths. It only ever stops early.
	maxStripWalk = 50_000
	// maxStrippedNames bounds the paths a result names; the bytes count all.
	maxStrippedNames = 20
	// shedMinBudget is the request time that must remain to start shedding:
	// deleting a large node_modules takes a while, and a result the deadline
	// cuts off is deferred, so what was shed would go unrecorded.
	shedMinBudget = 30 * time.Second
)

// shedsFor reports whether an archived worktree held for this reason still
// gets its regenerable bulk deleted. These holds keep the directory for the
// work in it, which shedding never touches. Every other hold is about whose
// the directory is, or whether it can be inspected at all (unmanaged,
// unverified, in use, quarantined), or the user chose to keep it (kept): no
// file in it is touched.
func shedsFor(reason HeldReason) bool {
	switch reason {
	case ReasonDirty, ReasonUnpushed, ReasonUnreachable, ReasonData, ReasonOutsideFiles, ReasonNestedRepo:
		return true
	}
	return false
}

// shed deletes the regenerable paths of an archived worktree that is being
// kept, and records them on res. A regenerable path is one git ignores AND the
// allow-list says a build or tool recreates (rebuildablePath): node_modules,
// build output, forge and tool caches, the checkout's tmp/. Tracked files,
// untracked files that are not ignored, and ignored files not on the
// allow-list are never touched, and neither is any path holding a git
// repository other than a tool's clone. Unarchiving keeps working; the
// workspace just needs its install or generate step run again.
//
// It runs only on the automatic path, for a worktree that is idle, not in use
// and not kept by the user's "Keep everything" setting, and only inside
// checkouts this row's lock proves are its own.
func (r *Reclaimer) shed(ctx context.Context, w Worktree, sc scan, res *Result) {
	if res == nil || res.Outcome != OutcomeHeld || !shedsFor(res.Reason) || w.KeepFiles || w.State != StateArchived || w.Remove || sc.Truncated {
		return
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < shedMinBudget {
		return // the next pass sheds it
	}
	checkouts := ownCheckouts(ctx, w, sc)
	if len(checkouts) == 0 {
		return
	}
	if r.IdleFloor > 0 && r.now().Sub(idleSince(w.Path, checkouts)) < r.IdleFloor {
		return
	}
	if hold := r.inUse(ctx, w, false); hold != nil {
		return
	}
	for _, c := range checkouts {
		for _, rel := range regenerableTargets(ctx, c, sc.Repos) {
			if ctx.Err() != nil {
				break
			}
			full := filepath.Join(c, filepath.FromSlash(rel))
			size, _ := DirSize(ctx, full)
			if err := os.RemoveAll(full); err != nil {
				continue
			}
			res.StrippedBytes += size
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

// ownCheckouts lists the linked, registered checkouts in the scan that this
// row's reliant lock (active, archived or restored) proves are its own.
func ownCheckouts(ctx context.Context, w Worktree, sc scan) []string {
	var out []string
	for _, repo := range sc.Repos {
		if !isLinkedWorktree(repo) {
			continue
		}
		info, err := inspectLock(ctx, repo)
		if err != nil || !info.Registered || !info.Locked {
			continue
		}
		if p, ok := parseLock(info.Reason); ok && p.ID == w.ID {
			out = append(out, repo)
		}
	}
	return out
}

// regenerableTargets lists the paths under checkout, relative to it, that git
// ignores and rebuildablePath accepts, outermost first, skipping any that is,
// holds or sits in a repository of repos other than checkout itself.
func regenerableTargets(ctx context.Context, checkout string, repos []string) []string {
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
	var safe []string
	for _, t := range targets {
		full := filepath.Join(checkout, filepath.FromSlash(t))
		if !holdsRepo(full, checkout, repos) {
			safe = append(safe, t)
		}
	}
	return safe
}

// collectRegenerable walks one ignored path the way dataWalk.visit does,
// but collects the regenerable ones: a regenerable path is taken whole, and
// any other directory is judged by its contents.
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

// holdsRepo reports whether path is, contains or sits inside a repository
// nested in checkout (a tool's clone is not one, see toolClone): deleting it
// could delete history.
func holdsRepo(path, checkout string, repos []string) bool {
	for _, repo := range repos {
		if !within(checkout, repo) {
			continue
		}
		if samePath(repo, path) || within(path, repo) || within(repo, path) {
			return true
		}
	}
	return false
}
