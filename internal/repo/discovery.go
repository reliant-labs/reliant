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
// returns one Found per repo. If projectPath itself is a git repo, the
// scan stops at the root and returns just that one.
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
