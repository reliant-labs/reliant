// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// scanMaxEntries and scanBudget bound the whole-workspace walk. Hitting
	// either means the answer is unknown, and unknown holds the worktree.
	scanMaxEntries = 2_000_000
	scanBudget     = 45 * time.Second
	// shallowDepth bounds the cheap discovery used to LOCK live worktrees,
	// which never needs to see every nested repository.
	shallowDepth = 3
)

// scan is everything a whole-workspace walk found.
type scan struct {
	// Repos are the directories that hold a `.git` entry (file or directory),
	// at any depth, including inside ignored directories.
	Repos []string
	// Outside are files and symlinks that live outside every repository: at the
	// workspace root, or between it and a checkout.
	Outside []string
	// Truncated: the walk hit its budget, so Repos/Outside may be incomplete.
	Truncated bool
}

// scanWorkspace walks root without following symlinks. It does not skip any
// directory by name: a repository inside node_modules or an ignored tmp/ holds
// history that exists nowhere else. The one exception is a clone a tool made
// and makes again (toolClone), which is dropped from Repos after the walk.
func scanWorkspace(ctx context.Context, root string) scan {
	ctx, cancel := context.WithTimeout(ctx, scanBudget)
	defer cancel()
	var out scan
	entries := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		entries++
		if ctx.Err() != nil || entries > scanMaxEntries {
			out.Truncated = true
			return fs.SkipAll
		}
		if err != nil {
			// An unreadable directory is one we cannot vouch for.
			out.Truncated = true
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != root {
				return fs.SkipDir
			}
			if _, e := os.Lstat(filepath.Join(p, ".git")); e == nil {
				out.Repos = append(out.Repos, p)
			}
			return nil
		}
		if d.Name() == ".git" {
			return nil // a linked worktree's pointer file; the dir above is the repo
		}
		if !underAny(out.Repos, p) {
			out.Outside = append(out.Outside, p)
		}
		return nil
	})
	out.Repos = withoutToolClones(ctx, out.Repos)
	sort.Strings(out.Repos)
	return out
}

// toolCloneDirs are directories, relative to a checkout's root, whose direct
// children are git clones a tool made and can make again from their remote.
// pre-commit clones each hook repository at a pinned rev into
// $PRE_COMMIT_HOME/repo*, and control-plane's CI gate points PRE_COMMIT_HOME
// at <checkout>/.tools/pre-commit. Anything else with a .git inside a
// checkout stays a nested repository: its history may exist nowhere else.
var toolCloneDirs = []string{".tools/pre-commit"}

func withoutToolClones(ctx context.Context, repos []string) []string {
	out := repos[:0:0]
	for _, r := range repos {
		if !toolClone(ctx, r, repos) {
			out = append(out, r)
		}
	}
	return out
}

// toolClone reports whether repo is a direct child of a toolCloneDirs entry of
// the repository that contains it, AND that repository's git ignores it. Both
// are required: a tracked or unignored clone is the user's.
func toolClone(ctx context.Context, repo string, repos []string) bool {
	owner := "" // the innermost other repository containing repo
	for _, p := range repos {
		if p != repo && within(p, repo) && (owner == "" || within(owner, p)) {
			owner = p
		}
	}
	if owner == "" {
		return false
	}
	rel := relTo(owner, repo)
	segs := splitSegments(rel)
	listed := false
	for _, dir := range toolCloneDirs {
		want := splitSegments(dir)
		if len(segs) == len(want)+1 && strings.Join(segs[:len(want)], "/") == strings.Join(want, "/") {
			listed = true
		}
	}
	if !listed {
		return false
	}
	_, err := git(ctx, owner, nil, "check-ignore", "-q", "--", rel)
	return err == nil
}

// underAny reports whether p lies inside one of repos. Both come from the same
// walk, which never follows a symlink, so the comparison is lexical: a symlink
// inside a checkout is that checkout's file wherever it points. Resolving it
// (within does) turned a venv's bin/python3 -> /usr/bin/python3.12 into "a file
// outside every checkout", which holds a workspace for good.
func underAny(repos []string, p string) bool {
	for _, r := range repos {
		rel, err := filepath.Rel(r, p)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// shallowCheckouts finds the checkouts of a workspace cheaply, for locking: the
// request's rels when given, else every `.git` within shallowDepth.
func shallowCheckouts(root string, checkouts []Checkout) []string {
	if len(checkouts) > 0 {
		var out []string
		for _, c := range checkouts {
			p := filepath.Join(root, filepath.FromSlash(c.Rel))
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				out = append(out, p)
			}
		}
		return out
	}
	var found []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			found = append(found, dir)
		}
		if depth >= shallowDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != ".git" && e.Type()&os.ModeSymlink == 0 {
				walk(filepath.Join(dir, e.Name()), depth+1)
			}
		}
	}
	walk(root, 0)
	return found
}

// removeEmptyTree removes root and every directory under it, but only when the
// whole tree holds no file at all. os.Remove refuses a directory with anything
// in it, so a stray file anywhere keeps its directory and nothing is lost.
func removeEmptyTree(root string) bool {
	var dirs []string
	empty := true
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			empty = false
			return fs.SkipAll
		}
		dirs = append(dirs, p)
		return nil
	})
	if !empty {
		return false
	}
	return removeEmptyDirs(dirs)
}

// removeEmptyDirs removes the given directories bottom-up; any that is not
// empty stays.
func removeEmptyDirs(dirs []string) bool {
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	ok := true
	for _, d := range dirs {
		if os.Remove(d) != nil {
			ok = false
		}
	}
	return ok
}

// emptyDirsUnder lists root and every directory beneath it.
func emptyDirsUnder(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	return dirs
}
