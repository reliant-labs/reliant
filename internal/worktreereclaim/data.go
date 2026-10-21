// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// This is forge's worktree policy (forge/internal/storage/worktrees.go,
// DefaultWorktreeRebuildable and the never-list), copied rather than shared so
// reliant and forge agree on what an ignored file in a worktree is worth
// without importing each other. Keep the two in step. Reliant is ahead of forge
// on forge's own .forge/tmp and .forge/generate-cache.json, the .tools/
// convention, the checkout's tmp/, generated files and copies of the source
// checkout's files (dataWalk), which forge should adopt.

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

// maxCompareBytes bounds the size of an ignored file compared byte for byte
// with its copy in the source checkout. A larger one holds the worktree.
const maxCompareBytes int64 = 64 << 20

// dataWalk finds the ignored paths of one checkout that are the user's data:
// what removing the checkout would lose. An ignored path is NOT data when
//
//   - a build recreates it (rebuildablePath);
//   - it is a regular file whose first line carries the generated-code marker
//     ("Code generated ... DO NOT EDIT."), and the never-list does not name
//     it: a tool wrote it from inputs that outlive the worktree;
//   - it is a regular file byte-identical to the same path in the source
//     checkout (the repository's main working tree): a copy of a file that
//     exists elsewhere, which is how a worktree gets its .env or dev secrets.
//     This one applies to never-listed paths too, because nothing is lost.
//     A file that differs from the source's, even only by being older, is
//     data: it may hold a credential the source no longer has;
//   - it is inside another checkout, which is classified on its own.
//
// Judging these during the walk, not after it, matters: the walk stops at
// maxDataNames entries, and entries filtered out afterwards could fill the cap
// and hide real data behind them.
type dataWalk struct {
	checkout string
	nested   []string
	// source finds the source checkout ("" when there is none). It is asked
	// only when a file needs comparing, and once: most walks never need it,
	// and every git process a pass starts is budgeted.
	source func() string
	out    []string
}

// newDataWalk returns a walk of checkout whose source checkout is looked up
// on first use.
func newDataWalk(ctx context.Context, checkout string, nested []string) *dataWalk {
	return &dataWalk{checkout: checkout, nested: nested, source: sync.OnceValue(func() string { return sourceCheckout(ctx, checkout) })}
}

// visit judges one ignored path. git collapses a directory holding only
// ignored files into one entry (web/ for web/node_modules/), which would hide
// an allowlisted child, so an unmatched directory is judged by its contents.
// Walking stops at every allowlisted directory, so node_modules is never
// traversed.
func (d *dataWalk) visit(rel string) {
	if len(d.out) >= maxDataNames {
		return
	}
	if rebuildablePath(rel) {
		return
	}
	full := filepath.Join(d.checkout, filepath.FromSlash(rel))
	for _, n := range d.nested {
		if samePath(full, n) || within(n, full) {
			return
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		d.out = append(d.out, rel)
		return
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() && ((!neverListed(rel) && generatedFile(full)) || d.copyInSource(rel, full, info)) {
			return
		}
		d.out = append(d.out, rel)
		return
	}
	children, err := os.ReadDir(full)
	if err != nil || len(children) == 0 {
		d.out = append(d.out, rel)
		return
	}
	for _, child := range children {
		name := strings.TrimSuffix(rel, "/") + "/" + child.Name()
		if child.IsDir() {
			name += "/"
		}
		d.visit(name)
	}
}

// copyInSource reports whether the source checkout holds a separate,
// byte-identical copy of this file at the same path. The copy must resolve to
// a path inside the source checkout (a symlinked directory there could lead
// back into this worktree, or into another one being removed) and must not be
// this very file reached another way.
func (d *dataWalk) copyInSource(rel, full string, info os.FileInfo) bool {
	if info.Size() > maxCompareBytes || d.source == nil {
		return false
	}
	source := d.source()
	if source == "" {
		return false
	}
	src := filepath.Join(source, filepath.FromSlash(rel))
	si, err := os.Lstat(src)
	if err != nil || !si.Mode().IsRegular() || si.Size() != info.Size() || os.SameFile(si, info) {
		return false
	}
	if !within(source, src) || within(d.checkout, src) {
		return false
	}
	return sameBytes(full, src)
}

// sameBytes reports whether two files have identical contents. Any read error
// is "not the same".
func sameBytes(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return atEOF(ea) && atEOF(eb)
		}
	}
}

func atEOF(err error) bool { return err == io.EOF || err == io.ErrUnexpectedEOF }

// generatedMarker is the first line of a file a tool generated, in any comment
// syntax: the convention of https://go.dev/s/generatedcode, which forge and
// most code generators follow ("// Code generated by forge. DO NOT EDIT.").
var generatedMarker = regexp.MustCompile(`^\s*(//|#|--|;|/\*|<!--)\s*Code generated .*DO NOT EDIT\.`)

// generatedFile reports whether the file's first line is the generated-code
// marker.
func generatedFile(full string) bool {
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	return generatedMarker.MatchString(line)
}

// sourceCheckout is the main working tree of the repository a linked worktree
// belongs to, or "" when it has none (a bare repository) or git cannot say.
func sourceCheckout(ctx context.Context, checkout string) string {
	common, err := commonDir(ctx, checkout)
	if err != nil {
		return ""
	}
	out, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(out, "\n\n")
	root := ""
	for _, line := range strings.Split(first, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			root = strings.TrimPrefix(line, "worktree ")
		case line == "bare":
			return ""
		}
	}
	if root == "" || samePath(root, checkout) {
		return ""
	}
	return root
}

// neverListed reports whether the never-list names an ignored path.
func neverListed(p string) bool {
	segments := splitSegments(p)
	for _, deny := range neverRebuildable {
		if matchesRun(segments, deny) {
			return true
		}
	}
	return false
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
	if neverListed(p) {
		return false
	}
	segments := splitSegments(p)
	for _, pattern := range defaultRebuildable {
		if matchesRun(segments, pattern) {
			return true
		}
	}
	return false
}
