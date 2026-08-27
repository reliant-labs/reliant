// Copyright (c) 2025 Reliant Labs

// Package filetree is the single bounded directory walk behind every file-tree
// view in the product: the server's GetFileTree RPC, the daemon's fs.get_tree
// command, and the daemon's filesystem-change poller.
//
// It exists because those three surfaces each carried their own copy of the
// walk and their own copy of "directories we never descend into", and the
// copies had drifted. The weakest list guarded the only unbounded walk, so
// opening a large real-world project (a 9.9 GB / 106k-file Unity checkout)
// recursed through the entire tree — including the 8.2 GB build cache that the
// project's own .gitignore excludes — and exhausted the system-wide file
// table.
//
// The walk here is bounded three ways, in increasing order of authority:
//
//   - a canonical skip set of dependency/build/cache directory names,
//   - the repository's own .gitignore rules when the root is inside a git repo,
//   - a hard node budget (MaxTreeNodes) that stops the walk no matter what.
//
// The node budget is the real backstop. Depth alone does not save a caller from
// a single directory holding two hundred thousand entries, and a skip list only
// knows the names it was taught. The budget knows none of that and stops
// anyway; when it fires the result is marked Truncated rather than silently
// short.
package filetree

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/localfs"
)

const (
	// DefaultTreeDepth is how many levels below the walk root are returned when
	// a caller does not ask for a specific depth. Two levels is what a file
	// explorer needs to render a root listing with correct expand affordances
	// on its immediate children; everything deeper is fetched lazily on expand.
	DefaultTreeDepth = 2

	// MaxTreeNodes is the hard node budget for a single walk. Nothing — not
	// MaxDepth, not an unknown vendor directory, not a pathological single
	// directory — produces more nodes than this.
	MaxTreeNodes = 50000

	// MaxDepth asks for as deep a tree as the node budget allows. It is NOT
	// unlimited: the walk still stops at MaxTreeNodes and reports truncation.
	// There is deliberately no way to request a truly unbounded walk.
	MaxDepth = -1
)

// skipDirNames are directory names never descended into and never listed. It is
// the union of the two lists this package replaced plus the families they
// missed (Unity, Xcode, CocoaPods, Python venvs, Gradle, Terraform).
//
// Names are compared case-insensitively: Unity ships `Library/`, `Temp/` and
// `Logs/` with capitals, and its .gitignore spells them `[Ll]ibrary/`.
var skipDirNames = map[string]bool{
	".git":             true,
	".gradle":          true,
	".idea":            true,
	".next":            true,
	".nuxt":            true,
	".reliant":         true,
	".terraform":       true,
	".tox":             true,
	".venv":            true,
	".cache":           true,
	"__pycache__":      true,
	"bower_components": true,
	"build":            true,
	"builds":           true,
	"coverage":         true,
	"deriveddata":      true, // Xcode DerivedData
	"dist":             true,
	"jspm_packages":    true,
	"library":          true,
	"logs":             true,
	"node_modules":     true,
	"obj":              true,
	"pods":             true,
	"target":           true,
	"temp":             true,
	"tmp":              true,
	"vendor":           true,
	"venv":             true,
}

// IsSkippedDir reports whether a directory of this name is one of the
// dependency, build or cache directories the tree never descends into. The
// comparison is case-insensitive.
func IsSkippedDir(name string) bool {
	return skipDirNames[strings.ToLower(name)]
}

// SkipDirNames returns the canonical skip set as a slice, for diagnostics and
// tests. The order is unspecified.
func SkipDirNames() []string {
	out := make([]string, 0, len(skipDirNames))
	for name := range skipDirNames {
		out = append(out, name)
	}
	return out
}

// Node is one entry in a walked tree. It is deliberately transport-neutral:
// the server maps it onto reliantv1.FileNode and the daemon onto its JSON
// wire struct, so neither shape leaks into the walk.
type Node struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time

	// Children is populated only for directories above the depth boundary.
	Children []*Node

	// HasChildren is the expand hint for a directory at the depth boundary:
	// true when the directory holds at least one entry that this same walk
	// would have shown. It is what lets a UI draw a chevron without loading
	// the subtree, and it agrees with the walk's own filter by construction —
	// both go through include().
	HasChildren bool
}

// Result is a completed walk.
type Result struct {
	Nodes []*Node

	// NodeCount is the total number of nodes produced, at every level.
	NodeCount int

	// Truncated is true when the node budget stopped the walk early, so the
	// caller knows the tree it holds is a prefix rather than the whole thing.
	Truncated bool
}

// Options configures a walk.
type Options struct {
	// Root is the absolute directory to walk. The root itself is never
	// filtered — a caller that explicitly names a directory gets its contents
	// even if the skip set or .gitignore would have hidden it from a parent
	// listing.
	Root string

	// RelBase is the path prefix stamped onto every node's Path, so results
	// can be addressed in whatever space the caller works in ("" for daemon
	// results, the project-relative request path for the server).
	RelBase string

	// ShowHidden includes dot-prefixed entries. It does NOT disable the skip
	// set or .gitignore: the crash this package exists to prevent came in
	// through a caller that always requests hidden files and filters them
	// client-side.
	ShowHidden bool

	// Depth bounds how many levels of children are returned below Root:
	// 0 means DefaultTreeDepth, N>0 means N levels (1 = immediate children),
	// and MaxDepth (-1) means as deep as the node budget allows.
	Depth int

	// MaxNodes overrides the node budget. Zero or less means MaxTreeNodes.
	// A caller may lower it; it exists for tests and for callers that want a
	// tighter cap, not as an escape hatch to raise it beyond reason.
	MaxNodes int

	// FS is the filesystem to read through. Nil means the local OS.
	FS localfs.FS
}

// Walk performs a bounded, gitignore-aware walk of opts.Root.
//
// It fails only when the root itself cannot be read. Errors below the root
// (an unreadable subdirectory, a vanished entry, a malformed .gitignore) are
// skipped rather than propagated: a file tree that renders most of a project
// is worth more than an error page, and every ignore-side failure is fail-open
// so a walk never disappears because a rule could not be parsed.
func Walk(opts Options) (*Result, error) {
	filesystem := opts.FS
	if filesystem == nil {
		filesystem = localfs.New()
	}

	maxNodes := opts.MaxNodes
	if maxNodes <= 0 {
		maxNodes = MaxTreeNodes
	}

	depth := opts.Depth
	switch {
	case depth == 0:
		depth = DefaultTreeDepth
	case depth < 0:
		depth = MaxDepth
	}

	w := &walker{
		fs:         filesystem,
		showHidden: opts.ShowHidden,
		maxNodes:   maxNodes,
		ignore:     newGitIgnore(filesystem, opts.Root),
	}

	nodes, err := w.walk(opts.Root, opts.RelBase, w.ignore.rootSegments(), w.ignore.rootPatterns(), depth)
	if err != nil {
		return nil, err
	}

	return &Result{Nodes: nodes, NodeCount: w.count, Truncated: w.truncated}, nil
}

type walker struct {
	fs         localfs.FS
	showHidden bool
	maxNodes   int
	ignore     *gitIgnore

	count     int
	truncated bool
}

// walk lists dir and, budget permitting, its subdirectories.
//
// segs are dir's path segments relative to the git repository root (nil when
// no repository applies); patterns are the gitignore rules in force for dir,
// in increasing priority. remaining is how many more levels to descend:
// MaxDepth descends until the budget stops it, N>0 descends N levels.
func (w *walker) walk(dir, relPath string, segs []string, patterns []pattern, remaining int) ([]*Node, error) {
	entries, err := w.fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	patterns, m := w.ignore.forDir(w.fs, dir, segs, patterns)

	// probe is reused across entries for match checks. Anything that outlives
	// the iteration (a recursive call's segments) gets its own copy.
	probe := make([]string, len(segs)+1)
	copy(probe, segs)

	var nodes []*Node
	for _, entry := range entries {
		if w.count >= w.maxNodes {
			w.truncated = true
			break
		}

		name := entry.Name()
		isDir := entry.IsDir()
		probe[len(segs)] = name
		if !w.include(name, isDir, probe, m) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			// The entry vanished between the listing and the stat. Skip it
			// rather than failing the whole tree.
			continue
		}

		entryPath := filepath.Join(dir, name)
		node := &Node{
			Name:    name,
			Path:    filepath.Join(relPath, name),
			IsDir:   isDir,
			ModTime: info.ModTime(),
		}
		w.count++

		switch {
		case !isDir:
			node.Size = info.Size()

		// Descend while more than one level remains, or while the caller asked
		// for everything the budget allows.
		case remaining == MaxDepth || remaining > 1:
			childSegs := append([]string(nil), probe...)
			childDepth := remaining
			if remaining > 0 {
				childDepth = remaining - 1
			}
			children, err := w.walk(entryPath, node.Path, childSegs, patterns, childDepth)
			if err == nil {
				node.Children = children
			}
			node.HasChildren = len(node.Children) > 0

		// This is the depth boundary: no children, just the expand hint.
		default:
			childSegs := append([]string(nil), probe...)
			node.HasChildren = w.hasVisibleChildren(entryPath, childSegs, patterns)
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// include is the single predicate deciding whether an entry appears in the
// tree. The walk and the has_children probe both go through it, so a chevron
// can never promise children that expanding would not reveal.
func (w *walker) include(name string, isDir bool, segs []string, m matcher) bool {
	if isDir && IsSkippedDir(name) {
		return false
	}
	if !w.showHidden && strings.HasPrefix(name, ".") {
		return false
	}
	if m.match(segs, isDir) {
		return false
	}
	return true
}

// hasVisibleChildren reports whether dir holds at least one entry this walk
// would have shown, short-circuiting on the first match. It loads dir's own
// .gitignore for the same reason the walk does: the answer has to be the one
// an actual expand would give.
func (w *walker) hasVisibleChildren(dir string, segs []string, patterns []pattern) bool {
	entries, err := w.fs.ReadDir(dir)
	if err != nil {
		return false
	}

	_, m := w.ignore.forDir(w.fs, dir, segs, patterns)

	probe := make([]string, len(segs)+1)
	copy(probe, segs)
	for _, e := range entries {
		probe[len(segs)] = e.Name()
		if w.include(e.Name(), e.IsDir(), probe, m) {
			return true
		}
	}
	return false
}

// WalkHashable visits every entry under root that the tree would show, calling
// fn for each, subject to the same skip set and node budget as Walk. It exists
// for the daemon's filesystem-change poller, which needs a stable digest of the
// tree rather than the tree itself, and which used to walk without any bound at
// all on a five-second timer.
//
// It reports whether the budget truncated the walk, so a caller can tell a
// complete digest from a prefix.
func WalkHashable(root string, maxNodes int, fn func(path string, d fs.DirEntry) error) (truncated bool, err error) {
	if maxNodes <= 0 {
		maxNodes = MaxTreeNodes
	}

	count := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() && path != root && IsSkippedDir(d.Name()) {
			return filepath.SkipDir
		}
		if count >= maxNodes {
			truncated = true
			return filepath.SkipAll
		}
		count++
		return fn(path, d)
	})
	return truncated, err
}
