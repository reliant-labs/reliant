// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// This is forge's worktree policy (forge/internal/storage/worktrees.go,
// DefaultWorktreeRebuildable and the never-list), copied rather than shared so
// reliant and forge agree on what an ignored file in a worktree is worth
// without importing each other. Keep the two in step. Reliant is ahead of forge
// on forge's own .forge/tmp and .forge/generate-cache.json, the .tools/
// convention and the checkout's tmp/, which forge should adopt.

// defaultRebuildable lists ignored paths that a build recreates. An entry is
// one or more path segments (globs allowed per segment) matched anywhere in the
// ignored path, or, with a leading "/", only from the checkout's root.
var defaultRebuildable = []string{
	"node_modules", "dist", "bin", ".gocache", ".next", ".next-prod", ".turbo",
	"coverage", "coverage.out", "*.tsbuildinfo", "next-env.d.ts",
	"test-results", "playwright-report", "__pycache__", ".DS_Store",
	"kcl.mod.lock", "go.work", "go.work.sum",
	".forge/generating-build", ".forge/forge.lock", ".forge/render",
	".forge/logs", ".forge/workspace-base.tag",
	// forge's own scratch and its generate fixed-point cache.
	".forge/tmp", ".forge/generate-cache.json",
	// Non-JS build and tool caches, the same additions forge is making.
	"target", ".venv", "venv", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".gradle",
	// Pinned tools a project installs into its checkout (bin, stamps, tmp,
	// pre-commit's hook clones): `task tools` puts them back.
	"/.tools",
	// The checkout's scratch directory. A git repository inside it is still a
	// nested repository (see toolClone), and data under it is still data.
	"/tmp",
}

// neverRebuildable are paths that hold application data. They hold a worktree
// no matter what else is allowed.
var neverRebuildable = []string{
	"data", ".env", ".env.*", ".forge/hostinfra", "*.db", "*.sqlite", "secrets",
}

const maxDataNames = 20

// unrecognizedIgnored returns the ignored paths under rel that are not
// rebuildable. git collapses a directory holding only ignored files into one
// entry (web/ for web/node_modules/), which would hide an allowlisted child, so
// an unmatched directory is judged by its contents. Walking stops at every
// allowlisted directory, so node_modules is never traversed.
func unrecognizedIgnored(worktree, rel string, out *[]string) {
	if len(*out) >= maxDataNames {
		return
	}
	if rebuildablePath(rel) {
		return
	}
	full := filepath.Join(worktree, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.IsDir() {
		*out = append(*out, rel)
		return
	}
	children, err := os.ReadDir(full)
	if err != nil || len(children) == 0 {
		*out = append(*out, rel)
		return
	}
	for _, child := range children {
		name := strings.TrimSuffix(rel, "/") + "/" + child.Name()
		if child.IsDir() {
			name += "/"
		}
		unrecognizedIgnored(worktree, name, out)
	}
}

func splitSegments(p string) []string {
	p = strings.Trim(filepath.ToSlash(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchesRun(segments []string, pattern string) bool {
	want := splitSegments(pattern)
	if len(want) == 0 {
		return false
	}
	last := len(segments) - len(want)
	if strings.HasPrefix(pattern, "/") {
		last = min(last, 0) // anchored: only a run starting at the root
	}
	for start := 0; start <= last; start++ {
		ok := true
		for i, w := range want {
			if m, err := path.Match(w, segments[start+i]); err != nil || !m {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// rebuildablePath reports whether an ignored path is recreated by a build. The
// never-list is checked first so nothing can make data deletable.
func rebuildablePath(p string) bool {
	segments := splitSegments(p)
	for _, deny := range neverRebuildable {
		if matchesRun(segments, deny) {
			return false
		}
	}
	for _, pattern := range defaultRebuildable {
		if matchesRun(segments, pattern) {
			return true
		}
	}
	return false
}
