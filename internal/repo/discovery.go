// Copyright (c) 2025 Reliant Labs
//
// Package repo discovers and identifies git repositories nested inside a
// project directory. The legacy shape (project root == single git repo)
// falls out as a special case where one Repo is found at relative path "".
package repo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMaxDepth is the default depth at which Discover scans for nested
// git repos. Depth 0 is the project root, depth 1 is direct children, etc.
const DefaultMaxDepth = 2

// DefaultMaxEntries bounds how many directory entries one Discover visits.
// A multi-repo project to depth 2 is a few thousand; past this the scan stops
// and says so (Discovery.Truncated) instead of walking on.
const DefaultMaxEntries = 20_000

// skipDirs are directory names we never descend into when scanning. These
// are conventional sinks of nested-but-irrelevant content: dependency and
// build trees, caches, and macOS's per-user Library (a project rooted at a
// home directory would otherwise list everything under ~/Library).
var skipDirs = map[string]struct{}{
	"node_modules":     {},
	"vendor":           {},
	"target":           {},
	"dist":             {},
	"build":            {},
	"Library":          {},
	"Caches":           {},
	"DerivedData":      {},
	"Pods":             {},
	"__pycache__":      {},
	"bower_components": {},
}

// skipDir reports whether a scan stays out of a directory named name: a
// skipDirs entry, or a hidden directory (.cache, .npm, .reliant, .venv, a
// checkout's own .git, ...).
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, skip := skipDirs[name]
	return skip
}

// Found is a repo discovered on disk. It is not yet persisted; callers
// turn this into a *Repo via the store.
type Found struct {
	RelativePath string // "" for the project root itself
	Name         string // basename of the repo dir, or project name for root
	RemoteURL    string // origin.url, "" if absent or unreadable
}

// Options bounds a Discover. Zero values take the defaults.
type Options struct {
	// MaxDepth is how deep below the root to look (DefaultMaxDepth when <= 0).
	MaxDepth int
	// MaxEntries is the entry budget (DefaultMaxEntries when <= 0).
	MaxEntries int
	// Deadline, when set, stops the walk at that time with what it has. A
	// caller answering under its own deadline sets it a little before, so a
	// partial answer still arrives.
	Deadline time.Time
}

// Discovery is what a scan found.
type Discovery struct {
	Repos []Found
	// Truncated is true when the walk stopped before covering the tree: its
	// entry budget ran out, its deadline came, or its context ended. Repos is
	// what it found until then, and the caller must say the list is partial
	// rather than present it, or an empty one, as the whole answer.
	Truncated bool
	// TruncatedReason says which bound stopped it, as the cause in a sentence
	// "the search stopped early because <reason>".
	TruncatedReason string
}

// Discover walks projectPath looking for directories that contain a `.git`
// entry (file or dir; both indicate a git checkout). It returns one Found per
// git repository: checkouts sharing a git common dir (a main checkout plus its
// linked worktrees) collapse to the main checkout when it is in the scan, else
// to the first linked worktree seen. It never descends into a nested checkout
// or a skipped directory (skipDir). A root that is itself a checkout is still
// walked, for a multi-repo project whose root tracks shared files; it is
// returned alone when nothing is nested.
//
// The walk is bounded (Options) and a bound that stops it is reported, not
// hidden: prod's repo.discover on a project rooted at a home directory could
// not answer inside its caller's deadline. Remotes are read from each
// repository's config file, never by running git.
//
// An empty result is not an error: a project may legitimately contain no
// git repos (e.g. a docs folder), and project init must still succeed.
func Discover(ctx context.Context, projectPath string, opts Options) (Discovery, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}

	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return Discovery{}, fmt.Errorf("resolve project path: %w", err)
	}

	// The stat error is kept: its "no such file or directory" is how the
	// daemon's caller tells a missing project folder from a failed scan.
	info, err := os.Stat(abs)
	if err != nil {
		return Discovery{}, fmt.Errorf("project path: %w", err)
	}
	if !info.IsDir() {
		return Discovery{}, fmt.Errorf("project path is not a directory: %s", abs)
	}
	// WalkDir does not follow a symlinked root (it visits the link and stops),
	// so a project opened through a symlink would find none of its nested
	// repos. Walk the target; relative paths are the same either way.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	rootIsGit := isGitDir(abs)

	var (
		found   []Found
		idents  []checkoutIdent
		entries int
		stopped string
	)
	rootDepth := strings.Count(abs, string(filepath.Separator))

	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			stopped = "the request ended"
			return fs.SkipAll
		}
		if !opts.Deadline.IsZero() && !time.Now().Before(opts.Deadline) {
			stopped = "it ran out of time"
			return fs.SkipAll
		}
		if entries++; entries > maxEntries {
			stopped = fmt.Sprintf("it looked at %d entries", maxEntries)
			return fs.SkipAll
		}
		if walkErr != nil {
			// Permission errors on subdirs shouldn't kill the whole scan.
			if os.IsPermission(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		if !d.IsDir() || path == abs {
			// The root itself is handled after the walk.
			return nil
		}

		// Depth check: relative depth from project root.
		if strings.Count(path, string(filepath.Separator))-rootDepth > maxDepth {
			return fs.SkipDir
		}
		if skipDir(d.Name()) {
			return fs.SkipDir
		}

		if isGitDir(path) {
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				return nil
			}
			found = append(found, Found{
				RelativePath: rel,
				Name:         filepath.Base(path),
			})
			idents = append(idents, identifyCheckout(path))
			// Don't recurse into a discovered repo.
			return fs.SkipDir
		}
		return nil
	})

	if walkErr != nil {
		return Discovery{}, fmt.Errorf("scan project: %w", walkErr)
	}

	// If the root is a git repo and no nested repos were found, treat it as
	// a single-repo project (the common case). If nested repos were found,
	// this is a multi-repo project where the root may also be a git repo
	// (e.g. tracking shared config with children gitignored).
	var rootIdent checkoutIdent
	if rootIsGit {
		rootIdent = identifyCheckout(abs)
	}
	kept := dedupeByRepository(found, idents, rootIdent.commonDir)
	repos := make([]Found, 0, len(kept)+1)
	for _, i := range kept {
		f := found[i]
		f.RemoteURL = originURL(idents[i].commonDir)
		repos = append(repos, f)
	}
	if rootIsGit && len(repos) == 0 {
		repos = append(repos, Found{
			RelativePath: "",
			Name:         filepath.Base(abs),
			RemoteURL:    originURL(rootIdent.commonDir),
		})
	}

	out := Discovery{Repos: repos}
	if stopped != "" {
		out.Truncated = true
		out.TruncatedReason = stopped
	}
	return out, nil
}

// ContainsRepo reports whether Discover would find any repository under
// projectPath: the root is a checkout, or a directory within maxDepth that the
// scan enters (skipDir) is. It answers that yes/no without walking the rest:
// it stops at the first `.git` it sees, at once when the root has one.
//
// It needs no entry budget: it is project.check_git's answer on every
// GetProject, and its first hit ends it.
//
// An unreadable or missing path is an error, as it is for Discover.
func ContainsRepo(ctx context.Context, projectPath string, maxDepth int) (bool, error) {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return false, fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false, fmt.Errorf("project path: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("project path is not a directory: %s", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if isGitDir(abs) {
		return true, nil
	}
	rootDepth := strings.Count(abs, string(filepath.Separator))
	found := false
	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		if !d.IsDir() || path == abs {
			return nil
		}
		if strings.Count(path, string(filepath.Separator))-rootDepth > maxDepth {
			return fs.SkipDir
		}
		if skipDir(d.Name()) {
			return fs.SkipDir
		}
		if isGitDir(path) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return false, fmt.Errorf("scan project: %w", walkErr)
	}
	return found, nil
}

// isGitDir reports whether the given directory contains a `.git` entry.
// A `.git` directory indicates a normal checkout; a `.git` file indicates
// a worktree linked to a parent repo. Both count as a repo for our purposes.
func isGitDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	_ = info
	return true
}

// checkoutIdent identifies the repository a checkout belongs to.
type checkoutIdent struct {
	commonDir string // cleaned git common dir; "" when unparseable
	isMain    bool   // .git is a directory
}

// identifyCheckout resolves a checkout's git common dir by reading .git
// (and gitdir/commondir) directly, without exec'ing git. An unparseable
// .git file yields an empty commonDir, making the checkout its own identity.
func identifyCheckout(dir string) checkoutIdent {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return checkoutIdent{}
	}
	if info.IsDir() {
		return checkoutIdent{commonDir: canonicalPath(dotGit), isMain: true}
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return checkoutIdent{}
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return checkoutIdent{}
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if gitdir == "" {
		return checkoutIdent{}
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	common := gitdir
	if cd, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		if v := strings.TrimSpace(string(cd)); v != "" {
			if !filepath.IsAbs(v) {
				v = filepath.Join(gitdir, v)
			}
			common = v
		}
	}
	return checkoutIdent{commonDir: canonicalPath(common)}
}

func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// dedupeByRepository keeps one checkout per common dir, preferring the main
// checkout, else the first in walk order, and returns the indexes into found
// it keeps. Checkouts whose common dir equals rootCommon (the scan root's own
// repository) are dropped: the root already represents that repository.
// Output preserves walk order.
func dedupeByRepository(found []Found, idents []checkoutIdent, rootCommon string) []int {
	chosen := make(map[string]int, len(found)) // commonDir -> index into found
	for i, id := range idents {
		if id.commonDir == "" {
			continue
		}
		if id.commonDir == rootCommon {
			chosen[id.commonDir] = -1
			continue
		}
		cur, ok := chosen[id.commonDir]
		if !ok || (cur >= 0 && id.isMain && !idents[cur].isMain) {
			chosen[id.commonDir] = i
		}
	}
	out := make([]int, 0, len(found))
	for i := range found {
		if cd := idents[i].commonDir; cd != "" && chosen[cd] != i {
			continue
		}
		out = append(out, i)
	}
	return out
}
