// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Discovery for the machine sweep (sweep.go): every linked git worktree on
// the machine, whoever made it and wherever it lives.
//
// A Reliant row knows only the workspaces Reliant made; agents also run plain
// `git worktree add` into /tmp, ~/src, xdg run dirs and straight under the
// worktree root. Every one of those is still registered in its repository's
// `git worktree list`, so discovery asks git, not the database: it finds the
// repositories (anchors) and lists each one's worktrees. Anchors come from
// the configured roots (each `.git` file or directory found there names one)
// and from the repositories the caller already knows.

// SweepRoot is one directory tree discovery walks for repositories, to Depth
// levels below it.
type SweepRoot struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

// rootWalkMaxEntries bounds one root's walk.
const rootWalkMaxEntries = 50_000

// DefaultSweepRoots are where agents leave worktrees on a machine: the
// worktree root, the temp and cache dirs, and the home directory.
func DefaultSweepRoots() []SweepRoot {
	var roots []SweepRoot
	if root, err := DefaultRoot(); err == nil {
		roots = append(roots, SweepRoot{Path: root, Depth: 4})
	}
	roots = append(roots, SweepRoot{Path: os.TempDir(), Depth: 3})
	if dir, err := os.UserCacheDir(); err == nil {
		roots = append(roots, SweepRoot{Path: dir, Depth: 3})
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, SweepRoot{Path: home, Depth: 3})
	}
	return roots
}

// ParseSweepRoots reads a comma-separated list of path[:depth] entries; a
// missing depth is 3. An empty list is nil (the defaults).
func ParseSweepRoots(raw string) []SweepRoot {
	var roots []SweepRoot
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		depth := 3
		if i := strings.LastIndex(item, ":"); i > 0 {
			if d, err := strconv.Atoi(item[i+1:]); err == nil && d >= 0 {
				depth, item = d, item[:i]
			}
		}
		roots = append(roots, SweepRoot{Path: filepath.Clean(item), Depth: depth})
	}
	return roots
}

// walkRoots finds the repositories under roots: the common git dir each
// `.git` file (a linked worktree) or `.git` directory (a main checkout)
// names, and the main checkouts themselves. It never enters a repository,
// node_modules or a symlinked directory, and never runs git.
func walkRoots(ctx context.Context, roots []SweepRoot) (commonDirs, mains []string) {
	seenCommon, seenMain := map[string]bool{}, map[string]bool{}
	for _, root := range roots {
		base := filepath.Clean(root.Path)
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			continue
		}
		entries := 0
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			entries++
			if ctx.Err() != nil || entries > rootWalkMaxEntries {
				return fs.SkipAll
			}
			if err != nil || !d.IsDir() {
				return nil
			}
			name := d.Name()
			if p != base && (name == "node_modules" || name == ".git" || name == ".reclaim" || name == reclaimQuarantineDir) {
				return fs.SkipDir
			}
			if fi, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				switch {
				case fi.Mode().IsRegular():
					if c := commonDirFromGitFile(filepath.Join(p, ".git")); c != "" {
						if c = resolvePath(c); !seenCommon[c] {
							seenCommon[c] = true
							commonDirs = append(commonDirs, c)
						}
					}
				case fi.IsDir():
					c := resolvePath(filepath.Join(p, ".git"))
					if !seenCommon[c] {
						seenCommon[c] = true
						commonDirs = append(commonDirs, c)
					}
					if m := resolvePath(p); !seenMain[m] {
						seenMain[m] = true
						mains = append(mains, m)
					}
				}
				if p != base {
					return fs.SkipDir // a repository: its worktrees come from git
				}
			}
			if rel, err := filepath.Rel(base, p); err == nil && rel != "." && strings.Count(rel, string(filepath.Separator))+1 >= root.Depth {
				return fs.SkipDir
			}
			return nil
		})
	}
	sort.Strings(commonDirs)
	sort.Strings(mains)
	return commonDirs, mains
}

// worktreeEntry is one block of `git worktree list --porcelain`.
type worktreeEntry struct {
	Path     string
	Head     string
	Branch   string // refs/heads/<name>; "" when detached
	Bare     bool
	Detached bool
	Locked   bool
	Reason   string
	Prunable bool
}

// parseWorktreeList parses `git worktree list --porcelain` (not -z: lock
// reasons and paths with a newline are quoted by git there).
func parseWorktreeList(out string) []worktreeEntry {
	var list []worktreeEntry
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var e worktreeEntry
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				e.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				e.Head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch "):
				e.Branch = strings.TrimPrefix(line, "branch ")
			case line == "bare":
				e.Bare = true
			case line == "detached":
				e.Detached = true
			case line == "locked":
				e.Locked = true
			case strings.HasPrefix(line, "locked "):
				e.Locked, e.Reason = true, strings.TrimPrefix(line, "locked ")
			case line == "prunable" || strings.HasPrefix(line, "prunable "):
				e.Prunable = true
			}
		}
		if e.Path != "" {
			list = append(list, e)
		}
	}
	return list
}

// discovered is one linked worktree found by discovery.
type discovered struct {
	worktreeEntry
	CommonDir string
	// Main is the repository's main checkout ("" for a bare repository).
	Main string
}

// discoverWorktrees lists the linked worktrees of every anchor. A main
// checkout (each list's first entry), a bare entry and a registration whose
// directory is gone are never in the result. Each checkout appears once,
// keyed by its canonical path.
func discoverWorktrees(ctx context.Context, commonDirs []string) ([]discovered, []string) {
	var out []discovered
	var anchors []string
	seen := map[string]bool{}
	for _, c := range commonDirs {
		if ctx.Err() != nil {
			break
		}
		lctx, cancel := context.WithTimeout(ctx, time.Minute)
		list, err := git(lctx, "", nil, "--git-dir="+c, "worktree", "list", "--porcelain")
		cancel()
		if err != nil {
			continue
		}
		anchors = append(anchors, c)
		entries := parseWorktreeList(list)
		main := ""
		for i, e := range entries {
			if i == 0 {
				if !e.Bare {
					main = e.Path
				}
				continue
			}
			if e.Bare || e.Prunable {
				continue
			}
			key := resolvePath(e.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			e.Path = key
			out = append(out, discovered{worktreeEntry: e, CommonDir: c, Main: main})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, anchors
}

// checkoutScan is what one walk of a checkout found.
type checkoutScan struct {
	// Newest is the latest mtime of any entry below the checkout's root,
	// ignored output included, the root directory and the .git pointer
	// excluded (a move changes neither the content nor the activity).
	Newest time.Time
	// Nested are directories below the root that hold a .git entry.
	Nested []string
	// Special is a path that is neither a directory, a regular file nor a
	// symlink (a socket, a FIFO, a device).
	Special string
	// Truncated: the walk hit its budget; nothing it found is complete.
	Truncated bool
}

const (
	checkoutScanMaxEntries = 1_000_000
	checkoutScanBudget     = 2 * time.Minute
)

// scanCheckout walks a checkout without following symlinks, skipping
// nothing by name: a repository inside node_modules is still a nested
// repository, and a write to build output is still activity.
func scanCheckout(ctx context.Context, root string) checkoutScan {
	ctx, cancel := context.WithTimeout(ctx, checkoutScanBudget)
	defer cancel()
	var s checkoutScan
	entries := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		entries++
		if ctx.Err() != nil || entries > checkoutScanMaxEntries {
			s.Truncated = true
			return fs.SkipAll
		}
		if err != nil {
			s.Truncated = true
			return nil
		}
		if p == root {
			return nil
		}
		if d.Name() == ".git" {
			if filepath.Dir(p) == root {
				return skipIfDir(d)
			}
			s.Nested = append(s.Nested, filepath.Dir(p))
			return skipIfDir(d)
		}
		info, err := d.Info()
		if err != nil {
			s.Truncated = true
			return nil
		}
		if t := info.ModTime(); t.After(s.Newest) {
			s.Newest = t
		}
		mode := info.Mode()
		if !mode.IsDir() && !mode.IsRegular() && mode&os.ModeSymlink == 0 && s.Special == "" {
			s.Special = p
		}
		return nil
	})
	return s
}

func skipIfDir(d fs.DirEntry) error {
	if d.IsDir() {
		return fs.SkipDir
	}
	return nil
}
