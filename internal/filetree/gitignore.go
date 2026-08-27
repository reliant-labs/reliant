// Copyright (c) 2025 Reliant Labs

package filetree

import (
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/reliant-labs/reliant/internal/localfs"
)

// The gitignore rules are evaluated with go-git's gitignore package, which is
// already in this module's dependency graph. It is a real implementation of the
// format — negations, `**`, anchored and directory-only patterns, character
// classes — rather than the glob approximation a hand-rolled matcher would be.
//
// What this file adds is *when* the rules are read. go-git's own ReadPatterns
// walks the entire tree up front collecting .gitignore files, which is exactly
// the unbounded walk this package exists to eliminate. Instead the patterns for
// the walk root's ancestors are read once (bounded by path depth), and each
// directory's own .gitignore is read as the walk enters it — a file we were
// about to list anyway.
//
// Every failure here is fail-open. A missing repository, an unreadable
// .gitignore, a root outside any repository: all of them mean "no gitignore
// rules apply", never "the walk fails".

// maxRepoRootSearch bounds the walk up the directory chain looking for a
// repository root, so a pathological path can never spin.
const maxRepoRootSearch = 128

// pattern is one parsed gitignore rule.
type pattern = gitignore.Pattern

// matcher applies a set of gitignore rules given in increasing priority. The
// zero value (nil) matches nothing, which is how "no repository here" is
// represented all the way down the walk.
type matcher []pattern

// match reports whether segs (a path relative to the repository root, split
// into segments) is excluded. Rules are consulted from highest priority to
// lowest and the first one to speak wins, so a later `!negation` overrides an
// earlier exclusion exactly as git does.
func (m matcher) match(segs []string, isDir bool) bool {
	for i := len(m) - 1; i >= 0; i-- {
		if r := m[i].Match(segs, isDir); r > gitignore.NoMatch {
			return r == gitignore.Exclude
		}
	}
	return false
}

// gitIgnore holds the gitignore rules in force at a walk root. A nil or
// disabled value means no rules apply and every method degrades to a no-op.
type gitIgnore struct {
	// segs is the walk root's path relative to the repository root.
	segs []string
	// patterns are the rules inherited from the repository root down to the
	// walk root's PARENT. The walk root's own .gitignore is picked up by the
	// walk itself, so it is not duplicated here.
	patterns []pattern
	// on is false when the root is outside a repository, or inside a directory
	// the repository already ignores.
	on bool
}

func (g *gitIgnore) enabled() bool { return g != nil && g.on }

func (g *gitIgnore) rootSegments() []string {
	if !g.enabled() {
		return nil
	}
	return g.segs
}

func (g *gitIgnore) rootPatterns() []pattern {
	if !g.enabled() {
		return nil
	}
	return g.patterns
}

// forDir returns the rules in force inside dir — the inherited ones plus dir's
// own .gitignore — along with a matcher over them. segs is dir's path relative
// to the repository root, which is the domain a .gitignore living in dir is
// interpreted against.
func (g *gitIgnore) forDir(filesystem localfs.FS, dir string, segs []string, inherited []pattern) ([]pattern, matcher) {
	if !g.enabled() {
		return nil, nil
	}

	own := readIgnoreFile(filesystem, filepath.Join(dir, ".gitignore"), segs)
	if len(own) == 0 {
		return inherited, matcher(inherited)
	}

	combined := make([]pattern, 0, len(inherited)+len(own))
	combined = append(combined, inherited...)
	combined = append(combined, own...)
	return combined, matcher(combined)
}

// newGitIgnore resolves the gitignore rules that apply at root. It returns a
// disabled value — never an error — when no repository governs root.
func newGitIgnore(filesystem localfs.FS, root string) *gitIgnore {
	root = filepath.Clean(root)

	repoRoot, ok := findRepoRoot(filesystem, root)
	if !ok {
		return &gitIgnore{}
	}

	rel, err := filepath.Rel(repoRoot, root)
	if err != nil {
		return &gitIgnore{}
	}
	segs := splitSegments(rel)

	// .git/info/exclude is repository-local and applies from the root down.
	// It is the lowest priority source we consult; global and system excludes
	// are deliberately not read, since the tree should reflect the project,
	// not one developer's machine-wide preferences.
	patterns := readIgnoreFile(filesystem, filepath.Join(repoRoot, ".git", "info", "exclude"), nil)

	// Then every .gitignore from the repository root down to the walk root's
	// parent, in increasing priority.
	for i := 0; i < len(segs); i++ {
		domain := segs[:i]
		dir := filepath.Join(append([]string{repoRoot}, domain...)...)
		patterns = append(patterns, readIgnoreFile(filesystem, filepath.Join(dir, ".gitignore"), domain)...)
	}

	// A caller that names an ignored directory outright — "show me what is in
	// build/" — means it. Turning the rules off for that walk keeps explicit
	// navigation working; the static skip set still applies underneath.
	if len(segs) > 0 && matcher(patterns).match(segs, true) {
		return &gitIgnore{}
	}

	return &gitIgnore{segs: segs, patterns: patterns, on: true}
}

// findRepoRoot walks up from dir looking for a .git entry. It matches both a
// normal repository (.git directory) and a linked worktree or submodule (.git
// file), because either one means gitignore rules govern this path.
func findRepoRoot(filesystem localfs.FS, dir string) (string, bool) {
	for i := 0; i < maxRepoRootSearch; i++ {
		if _, err := filesystem.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

// readIgnoreFile parses one gitignore-format file. A missing or unreadable file
// yields no rules rather than an error: the walk must survive it.
func readIgnoreFile(filesystem localfs.FS, path string, domain []string) []pattern {
	data, err := filesystem.ReadFile(path)
	if err != nil {
		return nil
	}

	var ps []pattern
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(line, domain))
	}
	return ps
}

// splitSegments turns a relative path into the segment slice go-git's matcher
// expects. "." and empty paths yield no segments.
func splitSegments(rel string) []string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return nil
	}
	parts := strings.Split(rel, "/")
	out := parts[:0]
	for _, p := range parts {
		if p != "" && p != "." {
			out = append(out, p)
		}
	}
	return out
}
