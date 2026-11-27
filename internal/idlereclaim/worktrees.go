// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The node_modules strip of idle worktrees. A linked worktree under the
// worktree root that nothing reclaims (it has uncommitted or unpushed work,
// or a chat is still bound to it, or it is an agent's own `git worktree add`)
// keeps its node_modules for as long as it stays: on one machine the 420 of
// them came to ~50 GiB on disk. After WorktreeNodeModulesIdle without use,
// and with no process using the checkout, each git-ignored node_modules in
// it that passes the regenerable inventory is removed. Nothing else in the
// checkout is touched: its files, its git state, its other ignored files.
//
// Its last use is the latest of the checkout's newest file (node_modules and
// .git not descended; their own directories' times count), its git admin
// HEAD and reflog, and the daemon's record of work naming a path in its
// workspace (Deps.WorktreeUse). The server's own shed (Worktree.ShedAfterSeconds)
// covers live rows no chat runs in; this one does not ask whose the checkout
// is or what chat is bound to it, because removing a node_modules loses
// nothing an install does not put back.

const (
	// worktreeRootDepth bounds discovery: a Reliant workspace's checkout is
	// <root>/<project>/<name>-<id>/<repo>; agents' checkouts sit higher.
	worktreeRootDepth = 4
	// worktreeRootMaxDirs bounds the directories discovery visits.
	worktreeRootMaxDirs = 50_000
)

func (r *Reclaimer) worktreePass(ctx context.Context, dryRun bool) []Entry {
	var out []Entry
	for _, c := range linkedCheckouts(r.WorktreeRoot) {
		if ctx.Err() != nil {
			break
		}
		e, considered := r.worktreeCheckout(ctx, c, dryRun)
		if !considered {
			continue // active, or nothing to remove: not worth a report line
		}
		logEntry(ctx, "worktree", e)
		out = append(out, e)
	}
	return out
}

// worktreeCheckout decides, and unless dryRun does, the strip of one
// checkout. considered is false for a checkout used too recently or with no
// node_modules, which the report leaves out.
func (r *Reclaimer) worktreeCheckout(ctx context.Context, checkout string, dryRun bool) (Entry, bool) {
	now := r.now()
	quiet := r.floors().WorktreeNodeModulesIdle
	used := latest(shallowActivity(checkout), lastOf(r.WorktreeUse, checkout))
	if now.Sub(used) < quiet {
		return Entry{}, false
	}
	if r.NodeModules == nil {
		return Entry{}, false
	}
	targets := r.NodeModules(ctx, checkout)
	if len(targets) == 0 {
		return Entry{}, false
	}
	deep, unmeasured := checkoutNewest(ctx, checkout)
	used = latest(used, deep)
	idle := now.Sub(used)
	e := Entry{Path: checkout, LastUse: used, IdleHours: int(idle.Hours()), DryRun: dryRun}
	if idle < quiet {
		return Entry{}, false
	}
	if unmeasured != "" {
		e.Action, e.Reason = ActionHeld, unmeasured
		return e, true
	}
	if err := r.inUse(ctx, checkout); err != nil {
		e.Action, e.Reason = ActionHeld, "in use: "+err.Error()
		return e, true
	}
	var removedAbs []string
	for _, rel := range targets {
		if ctx.Err() != nil {
			break
		}
		t := filepath.Join(checkout, filepath.FromSlash(rel))
		if r.Inspect == nil {
			e.Held = append(e.Held, rel+": no inventory available")
			continue
		}
		n, newest, err := r.Inspect(ctx, t)
		if err != nil {
			e.Held = append(e.Held, rel+": "+err.Error())
			continue
		}
		if age := now.Sub(newest); age < quiet {
			e.Held = append(e.Held, fmt.Sprintf("%s: changed %s ago", rel, age.Round(time.Minute)))
			continue
		}
		if !dryRun {
			if err := r.underLease(ctx, checkout, r.WorktreeRoot, func(ctx context.Context) error {
				deep, why := checkoutNewest(ctx, checkout)
				if why != "" || now.Sub(latest(deep, shallowActivity(checkout), lastOf(r.WorktreeUse, checkout))) < quiet {
					return fmt.Errorf("checkout activity changed")
				}
				return worktreereclaim.RemoveInventoriedOutput(ctx, t, quiet, now, r.quarantine(ctx, checkout, func(ctx context.Context) error {
					if !untrackedOutput(ctx, checkout, t) {
						return fmt.Errorf("output contains tracked files or is not ignored")
					}
					return nil
				}))
			}); err != nil {
				e.Held = append(e.Held, rel+": "+err.Error())
				continue
			}
			removedAbs = append(removedAbs, t)
		}
		e.Bytes += n
		e.Removed = append(e.Removed, rel)
	}
	if !dryRun {
		r.Records.add(checkout, now, idle, removedAbs)
	}
	r.finishStrip(&e)
	return e, true
}

// linkedCheckouts lists the linked git worktrees (a .git file) under root,
// at most worktreeRootDepth deep, sorted. Directories starting with a dot
// (the worktree reclaim's .reclaim quarantine, the trash) are skipped, and
// a checkout is never descended.
func linkedCheckouts(root string) []string {
	root = filepath.Clean(root)
	var out []string
	dirs := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		dirs++
		if dirs > worktreeRootMaxDirs {
			return fs.SkipAll
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
			return fs.SkipDir
		}
		if info, err := os.Lstat(filepath.Join(p, ".git")); err == nil && info.Mode().IsRegular() {
			out = append(out, p)
			return fs.SkipDir
		}
		if strings.Count(relOf(root, p), "/") >= worktreeRootDepth-1 {
			return fs.SkipDir
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// shallowActivity is the cheap part of a checkout's last use: its own
// directory's time and its git dir's HEAD and reflog (not the index, which
// every `git status` a UI polls rewrites), for a linked worktree (its admin
// dir) or a repository (its .git). It lets a pass skip an active checkout
// without walking it.
func shallowActivity(checkout string) time.Time {
	var newest time.Time
	bump := func(p string) {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	bump(checkout)
	gitDir := adminDir(checkout)
	if gitDir == "" {
		if info, err := os.Lstat(filepath.Join(checkout, ".git")); err == nil && info.IsDir() {
			gitDir = filepath.Join(checkout, ".git")
		}
	}
	if gitDir != "" {
		bump(filepath.Join(gitDir, "HEAD"))
		bump(filepath.Join(gitDir, "logs", "HEAD"))
	}
	return newest
}

// checkoutNewest walks a checkout for its newest entry, not descending .git
// or any node_modules. unmeasured is why the walk cannot vouch for it.
func checkoutNewest(ctx context.Context, checkout string) (newest time.Time, unmeasured string) {
	entries := 0
	_ = filepath.WalkDir(checkout, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			unmeasured = "stopped"
			return fs.SkipAll
		}
		if err != nil {
			unmeasured = "cannot read " + relOf(checkout, p)
			return fs.SkipAll
		}
		entries++
		if entries > maxScanEntries {
			unmeasured = "too large to inspect completely"
			return fs.SkipAll
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if d.IsDir() && p != checkout && (d.Name() == ".git" || d.Name() == "node_modules") {
			return fs.SkipDir
		}
		return nil
	})
	return newest, unmeasured
}
