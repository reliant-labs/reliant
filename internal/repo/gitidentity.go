// Copyright (c) 2025 Reliant Labs

package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// gitIdentity classifies the checkout at dir by reading its `.git` entry —
// the same files git itself reads, no process spawned.
//
//   - `.git` directory: a primary checkout; common is that directory.
//   - `.git` file `gitdir: G` where G/commondir exists: a linked worktree;
//     common is the directory commondir points at.
//   - `.git` file without a commondir (submodule, --separate-git-dir): a
//     primary checkout; common is G.
//
// ok is false when the entry is missing, unreadable or malformed; callers
// fail open and keep such directories.
func gitIdentity(dir string) (common string, linked bool, ok bool) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", false, false
	}
	if info.IsDir() {
		return canonicalPath(dotGit), false, true
	}

	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false, false
	}
	line := strings.TrimSpace(string(data))
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	gitdir, found := strings.CutPrefix(line, "gitdir:")
	gitdir = strings.TrimSpace(gitdir)
	if !found || gitdir == "" {
		return "", false, false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)

	cd, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return canonicalPath(gitdir), false, true
	}
	rel := strings.TrimSpace(string(cd))
	if rel == "" {
		return "", false, false
	}
	if !filepath.IsAbs(rel) {
		rel = filepath.Join(gitdir, rel)
	}
	return canonicalPath(rel), true, true
}

// canonicalPath resolves symlinks so equal directories compare equal
// (macOS /private/var, symlinked homes); on error it falls back to Clean.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
