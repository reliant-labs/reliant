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
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultMaxDepth is the default depth at which Discover scans for nested
// git repos. Depth 0 is the project root, depth 1 is direct children, etc.
const DefaultMaxDepth = 2

// skipDirs are directory names we never descend into when scanning. These
// are conventional sinks of nested-but-irrelevant content.
var skipDirs = map[string]struct{}{
	".reliant":     {},
	"node_modules": {},
	"vendor":       {},
	"target":       {},
	"dist":         {},
	"build":        {},
	".next":        {},
	".venv":        {},
}

// Found is a repo discovered on disk. It is not yet persisted; callers
// turn this into a *Repo via the store.
type Found struct {
	RelativePath string // "" for the project root itself
	Name         string // basename of the repo dir, or project name for root
	RemoteURL    string // origin.url, "" if absent or unreadable
}

// Discover walks projectPath up to maxDepth looking for directories that
// contain a `.git` entry (file or dir; both indicate a git checkout). It
// returns one Found per git repository: checkouts sharing a git common dir
// (a main checkout plus its linked worktrees) collapse to the main checkout
// when it is in the scan, else to the first linked worktree seen. If
// projectPath itself is a git repo, the scan stops at the root and returns
// just that one.
//
// If maxDepth <= 0, DefaultMaxDepth is used.
//
// An empty result is not an error: a project may legitimately contain no
// git repos (e.g. a docs folder), and project init must still succeed.
func Discover(ctx context.Context, projectPath string, maxDepth int) ([]Found, error) {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}

	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}

	if !isDir(abs) {
		return nil, fmt.Errorf("project path is not a directory: %s", abs)
	}

	rootIsGit := isGitDir(abs)

	var found []Found
	var idents []checkoutIdent
	rootDepth := strings.Count(abs, string(filepath.Separator))

	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission errors on subdirs shouldn't kill the whole scan.
			if os.IsPermission(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}

		// Depth check: relative depth from project root.
		depth := strings.Count(path, string(filepath.Separator)) - rootDepth
		if depth > maxDepth {
			return fs.SkipDir
		}

		name := d.Name()
		if path != abs {
			if _, skip := skipDirs[name]; skip {
				return fs.SkipDir
			}
		}

		// Skip the root itself — we handle it after the walk.
		if path == abs {
			return nil
		}

		if isGitDir(path) {
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				return nil
			}
			found = append(found, Found{
				RelativePath: rel,
				Name:         filepath.Base(path),
				RemoteURL:    readRemoteURL(ctx, path),
			})
			idents = append(idents, identifyCheckout(path))
			// Don't recurse into a discovered repo.
			return fs.SkipDir
		}
		return nil
	})

	if walkErr != nil {
		return nil, fmt.Errorf("scan project: %w", walkErr)
	}

	// If the root is a git repo and no nested repos were found, treat it as
	// a single-repo project (the common case). If nested repos were found,
	// this is a multi-repo project where the root may also be a git repo
	// (e.g. tracking shared config with children gitignored).
	found = dedupeByRepository(found, idents, rootIdentity(abs, rootIsGit))

	if rootIsGit && len(found) == 0 {
		return []Found{{
			RelativePath: "",
			Name:         filepath.Base(abs),
			RemoteURL:    readRemoteURL(ctx, abs),
		}}, nil
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// readRemoteURL returns origin.url at the given repo path, or "" if the
// command fails (no remote, not a repo, etc). It never returns an error
// because a missing remote is not a discovery failure.
func readRemoteURL(ctx context.Context, repoPath string) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkoutIdent identifies the repository a checkout belongs to.
type checkoutIdent struct {
	commonDir string // cleaned git common dir; "" when unparseable
	isMain    bool   // .git is a directory
}

func rootIdentity(root string, rootIsGit bool) string {
	if !rootIsGit {
		return ""
	}
	return identifyCheckout(root).commonDir
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

// dedupeByRepository keeps one Found per common dir, preferring the main
// checkout, else the first in walk order. Checkouts whose common dir equals
// rootCommon (the scan root's own repository) are dropped: the root already
// represents that repository. Output preserves walk order.
func dedupeByRepository(found []Found, idents []checkoutIdent, rootCommon string) []Found {
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
	out := make([]Found, 0, len(found))
	for i, f := range found {
		if cd := idents[i].commonDir; cd != "" && chosen[cd] != i {
			continue
		}
		out = append(out, f)
	}
	return out
}
