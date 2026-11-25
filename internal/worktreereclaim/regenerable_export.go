// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The regenerable-output inspection, for a deleter outside this package
// (internal/idlereclaim): the scratch-directory lifecycle and the
// node_modules strip of idle kept worktrees. They decide when; this package
// keeps deciding what a regenerable directory is and what holds one.

// InspectRegenerable inventories the regenerable directory dir (a
// node_modules, a build output, a tool cache) with the shedding inventory's
// holds: a protected descendant (.env, data, secrets, *.sqlite) a package
// manager's install record does not account for, a nested repository, a
// mount, a special file. It returns the bytes the directory holds and the
// newest modification time in it. Any error is a hold: the directory is not
// to be removed. Linux only; elsewhere every directory holds.
func InspectRegenerable(ctx context.Context, dir string) (bytes int64, newest time.Time, err error) {
	tree, err := inspectRegenerableTree(ctx, dir)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer tree.close()
	return tree.bytes, tree.newest, nil
}

// Quarantine is how RemoveInventoriedOutput and RemoveInventoriedScratch
// take an inventoried directory away. Deletion is not atomic, so the
// directory is first renamed out of its path (one atomic step): from then on
// no new process can reach it by its old name, and a process that was
// already inside it is seen at its parked path. Only then does the final
// use check run, and it runs again at deletion boundaries. A check that
// fails stops the deletion and renames what is left back.
type Quarantine struct {
	// Trash is the directory the target is parked in, on the target's
	// filesystem; it is created if missing. The caller holds its admission
	// lease over it too, and leaves nothing else in it.
	Trash string
	// Authorize runs once, after the inventory and before the target is
	// parked: the caller's content checks (git state, a .keep, activity).
	// An error holds the target where it is.
	Authorize func(ctx context.Context) error
	// InUse reports process or lease use of any of paths: the target's
	// original path and its parked path. It runs once the target is parked,
	// before anything is deleted (the final authorization), and again at
	// deletion boundaries, at most every CheckEvery (0: before every
	// entry). ctx exempts the inventory's own descriptors. Required.
	InUse func(ctx context.Context, paths []string) error
	// CheckEvery paces the deletion-boundary checks. Each entry is still
	// checked against its inventory between them.
	CheckEvery time.Duration
}

// RemoveInventoriedOutput removes the regenerable directory dir through q,
// keeping its descriptor-anchored inventory alive from inspection through
// deletion. It holds when anything in dir changed within quiet of now.
func RemoveInventoriedOutput(ctx context.Context, dir string, quiet time.Duration, now time.Time, q Quarantine) error {
	tree, err := inspectRegenerableTree(ctx, dir)
	if err != nil {
		return err
	}
	defer tree.close()
	if now.Sub(tree.newest) < quiet {
		return errors.New("output changed recently")
	}
	return removeQuarantined(ctx, tree, dir, q)
}

// RemoveInventoriedScratch removes a scratch tree through q, holding on any
// mount, unreadable entry or inventory change. q.Authorize must check every
// git root in it.
func RemoveInventoriedScratch(ctx context.Context, dir string, q Quarantine) error {
	tree, err := inspectScratchTree(ctx, dir)
	if err != nil {
		return err
	}
	defer tree.close()
	return removeQuarantined(ctx, tree, dir, q)
}

// removeQuarantined authorizes, parks, checks use, and deletes tree; on any
// failure after parking it puts what is left back at dir.
func removeQuarantined(ctx context.Context, tree *retirementTree, dir string, q Quarantine) error {
	if q.InUse == nil || q.Trash == "" {
		return errors.New("no quarantine for the removal")
	}
	original, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if q.Authorize != nil {
		if err := q.Authorize(withInspectionFiles(ctx, tree.inspectionFiles())); err != nil {
			return err
		}
	}
	parked, err := tree.park(q.Trash)
	if err != nil {
		return fmt.Errorf("parking it: %w", err)
	}
	defer parked.close()
	paths := []string{original, tree.path}
	inspection := append(tree.inspectionFiles(), parked.files()...)
	paced := &pacedCheck{every: q.CheckEvery, check: func() error {
		return q.InUse(withInspectionFiles(ctx, inspection), paths)
	}}
	// The first call always checks: the final authorization, with the
	// directory already unreachable by its old name.
	err = paced.run()
	if err == nil {
		err = tree.remove(ctx, paced.run)
		if err == nil {
			return nil
		}
		err = fmt.Errorf("deletion stopped: %w", err)
	}
	if rerr := tree.unpark(parked); rerr != nil {
		return fmt.Errorf("%w; what is left stays parked at %s: %v", err, tree.path, rerr)
	}
	return err
}

// NodeModulesTargets lists the node_modules directories of checkout, relative
// to it with forward slashes, outermost first, that git ignores (so none is
// tracked) and the regenerable allow-list accepts. A node_modules inside
// another regenerable directory is not listed on its own: the outer one is.
func NodeModulesTargets(ctx context.Context, checkout string) []string {
	var out []string
	for _, rel := range regenerableTargets(ctx, checkout) {
		rel = strings.TrimSuffix(rel, "/")
		if path.Base(rel) == "node_modules" {
			out = append(out, rel)
		}
	}
	return out
}
