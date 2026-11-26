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
//
// A caller with a deadline (Options.Deadline) also bounds the walk in time:
// past it, no further directory below the root is read, and every directory not
// yet entered is returned unexpanded for the UI to load on demand. The root is
// always listed. An individual in-progress ReadDir cannot be interrupted, so
// this is a best-effort time bound, not a guarantee on a slow filesystem.
//
//forge:exclude-contract: bounded directory walk over a local root (skip set, .gitignore, node budget); local filesystem reads only, no network
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

	// Link is set when the entry is a symbolic link; nil otherwise. IsDir,
	// Size and ModTime then describe what the link resolves to, so a link to a
	// directory is a directory a UI expands like any other.
	Link *Link
}

// Link describes a symbolic link in the tree.
//
// The walk never descends into a linked directory, whatever the depth: a link
// can lead out of the project, into a credential store, or back up to one of
// its own ancestors, and following it unasked is how a bounded walk becomes an
// unbounded one. A linked directory is always a depth boundary instead, with
// HasChildren probed through the link, and its contents load only when a
// caller names it as a walk root — where the caller's own scope checks apply.
type Link struct {
	// Target is the link's own text, as readlink reports it ("" when the
	// filesystem cannot read links).
	Target string
	// Status says whether the link can be followed.
	Status LinkStatus
}

// LinkStatus says whether a symbolic link can be followed.
type LinkStatus int

const (
	// LinkResolved is a link to an existing file or directory.
	LinkResolved LinkStatus = iota + 1
	// LinkBroken is a link whose target cannot be resolved: it does not
	// exist, or the link is part of a cycle of links. It is listed as a file
	// and is not expandable.
	LinkBroken
	// LinkLoop is a link to a directory that contains it (the directory
	// itself or one of its ancestors). Expanding it would list a tree the
	// user is already in, without end, so it is listed but not expandable.
	LinkLoop
	// LinkDenied is a link to a directory the caller's DenyDir refuses (a
	// credential store, or a place outside a confined caller's root). It is
	// listed but not expandable.
	LinkDenied
)

// Expandable reports whether a UI should offer to expand n: a directory that
// is not a link the walk refuses to follow, and that holds something to show.
func (n *Node) Expandable() bool {
	if !n.IsDir {
		return false
	}
	if n.Link != nil && n.Link.Status != LinkResolved {
		return false
	}
	return n.HasChildren || len(n.Children) > 0
}

// Result is a completed walk.
type Result struct {
	Nodes []*Node

	// NodeCount is the total number of nodes produced, at every level.
	NodeCount int

	// Truncated is true when the node budget or the deadline stopped the walk
	// early, so the caller knows the tree it holds is a prefix rather than the
	// whole thing.
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
	// set or the node budget: the crash this package exists to prevent came in
	// through a caller that always requests hidden files and filters them
	// client-side.
	//
	// It does reveal hidden directories in full. A hidden directory is shown
	// even when .gitignore excludes it, and .gitignore does not apply inside
	// one: a tool's state directory (.claude, .vscode, .forge, .env.d) is
	// usually ignored as a whole or with a `dir/*` rule, so applying the rules
	// inside it listed the directory and hid everything in it, and the tree
	// drew it as a folder with nothing to open.
	ShowHidden bool

	// Depth bounds how many levels of children are returned below Root:
	// 0 means DefaultTreeDepth, N>0 means N levels (1 = immediate children),
	// and MaxDepth (-1) means as deep as the node budget allows.
	Depth int

	// MaxNodes overrides the node budget. Zero or less means MaxTreeNodes.
	// A caller may lower it; it exists for tests and for callers that want a
	// tighter cap, not as an escape hatch to raise it beyond reason.
	MaxNodes int

	// Deadline, when set, is when the walk stops reading directories. The
	// root is always listed, and a directory already being listed is listed
	// in full; past the deadline, every directory not yet entered is returned
	// with no Children and HasChildren set (it may turn out to be empty when
	// expanded), and the result is marked Truncated. Zero means no deadline.
	// An individual ReadDir already in progress cannot be interrupted; the
	// deadline is checked before and after each listing (including probes).
	//
	// It exists for the daemon's fs.get_tree, whose caller gives up after its
	// request deadline: on a slow or busy disk a walk inside the node budget
	// still outlasted that (prod, 2026-11-25: fs.get_tree took 7-10s on one
	// machine, and every request that crossed 10s failed with nothing shown).
	// A partial tree delivered in time beats a whole one nobody receives.
	Deadline time.Time

	// FS is the filesystem to read through. Nil means the local OS.
	FS localfs.FS

	// DenyDir, when set, is asked about every symbolic link to a directory
	// before the walk probes through it, with the link's path. A true answer
	// lists the link as LinkDenied and reads nothing behind it. The daemon
	// sets it to its credential-store guard and to a confined caller's root.
	DenyDir func(path string) bool
}

// linkFS is the part of a filesystem that reads symbolic links. localfs.Local
// implements it; a filesystem without it still lists links by what they
// resolve to, but cannot report their text or detect a link to an ancestor.
type linkFS interface {
	Readlink(name string) (string, error)
	EvalSymlinks(path string) (string, error)
}

// now is the walk's clock, a variable so tests can run out of time on cue.
var now = time.Now

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
		deadline:   opts.Deadline,
		ignore:     newGitIgnore(filesystem, opts.Root),
		denyDir:    opts.DenyDir,
	}
	w.links, _ = filesystem.(linkFS)

	// A root inside a hidden directory is walked as that directory's own
	// subtree would be: with show_hidden, no gitignore rules apply there.
	rules := w.ignore.enabled() && (!w.showHidden || !anyHidden(w.ignore.rootSegments()))

	nodes, err := w.walk(opts.Root, opts.RelBase, w.ignore.rootSegments(), w.ignore.rootPatterns(), rules, depth, true)
	if err != nil {
		return nil, err
	}

	return &Result{Nodes: nodes, NodeCount: w.count, Truncated: w.truncated}, nil
}

type walker struct {
	fs         localfs.FS
	links      linkFS // nil when fs cannot read links
	showHidden bool
	maxNodes   int
	deadline   time.Time
	ignore     *gitIgnore
	denyDir    func(string) bool

	count     int
	truncated bool
	// expired latches once the deadline has passed, so every directory after
	// that point is treated alike.
	expired bool
}

// outOfTime reports whether the deadline has passed, marking the result
// truncated the first time it is found to have.
func (w *walker) outOfTime() bool {
	if w.expired {
		return true
	}
	if w.deadline.IsZero() || now().Before(w.deadline) {
		return false
	}
	w.expired = true
	w.truncated = true
	return true
}

// isHidden reports whether name is a dot-prefixed entry.
func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

// anyHidden reports whether any segment of a path is hidden.
func anyHidden(segs []string) bool {
	for _, s := range segs {
		if isHidden(s) {
			return true
		}
	}
	return false
}

// rulesBelow reports whether gitignore rules apply inside the directory name,
// given whether they apply in its parent. With show_hidden a hidden directory
// is revealed in full (see Options.ShowHidden), so the rules stop at it.
func (w *walker) rulesBelow(rules bool, name string) bool {
	return rules && (!w.showHidden || !isHidden(name))
}

// matcherFor returns the rules in force inside dir and a matcher over them, or
// nothing when rules are off for this subtree.
func (w *walker) matcherFor(dir string, segs []string, patterns []pattern, rules bool) ([]pattern, matcher) {
	if !rules {
		return nil, nil
	}
	return w.ignore.forDir(w.fs, dir, segs, patterns)
}

// entryKind is what the walk needs to know about one directory entry before
// deciding whether to show it: whether it is (or links to) a directory, and,
// for a symbolic link, what it resolves to.
type entryKind struct {
	isDir bool
	link  *Link
	// target is the resolved entry's info for a link that resolved; nil
	// otherwise, in which case the entry's own info is used.
	target fs.FileInfo
}

// classify decides an entry's kind. A plain entry is what ReadDir says it is.
// A symbolic link is what it points at: fs.DirEntry.IsDir reports the link
// itself, never its target, which listed every link to a directory as a file
// the tree could not open.
func (w *walker) classify(dir string, entry fs.DirEntry) entryKind {
	if entry.Type()&fs.ModeSymlink == 0 {
		return entryKind{isDir: entry.IsDir()}
	}
	entryPath := filepath.Join(dir, entry.Name())
	link := &Link{Status: LinkResolved}
	if w.links != nil {
		link.Target, _ = w.links.Readlink(entryPath)
	}
	target, err := w.fs.Stat(entryPath)
	if err != nil {
		// Dangling, or a cycle of links (ELOOP): nothing to follow.
		link.Status = LinkBroken
		return entryKind{link: link}
	}
	if !target.IsDir() {
		return entryKind{link: link, target: target}
	}
	switch {
	case w.linksToAncestor(dir, entryPath):
		link.Status = LinkLoop
	case w.denyDir != nil && w.denyDir(entryPath):
		link.Status = LinkDenied
	}
	return entryKind{isDir: true, link: link, target: target}
}

// linksToAncestor reports whether the directory link at linkPath, which lives
// in dir, resolves to dir itself or to one of dir's ancestors.
func (w *walker) linksToAncestor(dir, linkPath string) bool {
	if w.links == nil {
		return false
	}
	realTarget, err := w.links.EvalSymlinks(linkPath)
	if err != nil {
		return false
	}
	realDir, err := w.links.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return withinDir(realDir, realTarget)
}

// withinDir reports whether p is base or lies beneath it.
func withinDir(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// walk lists dir and, budget permitting, its subdirectories.
//
// segs are dir's path segments relative to the git repository root (nil when
// no repository applies); patterns are the gitignore rules in force for dir,
// in increasing priority. remaining is how many more levels to descend:
// MaxDepth descends until the budget stops it, N>0 descends N levels.
//
// rules says whether gitignore rules apply in dir at all; it turns off below
// a hidden directory when show_hidden is set (rulesBelow).
func (w *walker) walk(dir, relPath string, segs []string, patterns []pattern, rules bool, remaining int, root bool) ([]*Node, error) {
	// Always list the root, even if the request has already expired. Check at
	// the read boundary as well as at the caller: time may pass between them.
	if w.outOfTime() && !root {
		return nil, nil
	}
	entries, err := w.fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// ReadDir cannot be interrupted. Record an overrun even if this listing
	// contains only files (or is empty), and do not start any child reads.
	w.outOfTime()

	patterns, m := w.matcherFor(dir, segs, patterns, rules)

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
		kind := w.classify(dir, entry)
		isDir := kind.isDir
		probe[len(segs)] = name
		if !w.include(name, isDir, probe, m) {
			continue
		}

		info := kind.target
		if info == nil {
			info, err = entry.Info()
			if err != nil {
				// The entry vanished between the listing and the stat. Skip
				// it rather than failing the whole tree.
				continue
			}
		}

		entryPath := filepath.Join(dir, name)
		node := &Node{
			Name:    name,
			Path:    filepath.Join(relPath, name),
			IsDir:   isDir,
			ModTime: info.ModTime(),
			Link:    kind.link,
		}
		w.count++

		childRules := w.rulesBelow(rules, name)
		switch {
		case !isDir:
			node.Size = info.Size()

		// A linked directory is never followed by the walk itself (see Link):
		// a loop or a refused target shows no chevron, and a resolved one is
		// a depth boundary at every depth, probed through the link.
		case kind.link != nil:
			if kind.link.Status == LinkResolved {
				childSegs := append([]string(nil), probe...)
				node.HasChildren = w.hasVisibleChildren(entryPath, childSegs, patterns, childRules)
			}

		// Out of time: leave the directory unread, for the UI to expand on
		// demand. Its contents are unknown, so the chevron is offered.
		case w.outOfTime():
			node.HasChildren = true

		// Descend while more than one level remains, or while the caller asked
		// for everything the budget allows.
		case remaining == MaxDepth || remaining > 1:
			childSegs := append([]string(nil), probe...)
			childDepth := remaining
			if remaining > 0 {
				childDepth = remaining - 1
			}
			children, err := w.walk(entryPath, node.Path, childSegs, patterns, childRules, childDepth, false)
			if err == nil {
				node.Children = children
			}
			node.HasChildren = len(node.Children) > 0 || w.expired

		// This is the depth boundary: no children, just the expand hint.
		default:
			childSegs := append([]string(nil), probe...)
			node.HasChildren = w.hasVisibleChildren(entryPath, childSegs, patterns, childRules)
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
	if !w.showHidden && isHidden(name) {
		return false
	}
	// A hidden directory is shown even when ignored (Options.ShowHidden); its
	// contents then come up with the rules off, through rulesBelow.
	if m.match(segs, isDir) && (!w.showHidden || !isDir || !isHidden(name)) {
		return false
	}
	return true
}

// hasVisibleChildren reports whether dir holds at least one entry this walk
// would have shown, short-circuiting on the first match. It loads dir's own
// .gitignore for the same reason the walk does: the answer has to be the one
// an actual expand would give.
func (w *walker) hasVisibleChildren(dir string, segs []string, patterns []pattern, rules bool) bool {
	if w.outOfTime() {
		return true // Unknown contents: let the UI expand on demand.
	}
	entries, err := w.fs.ReadDir(dir)
	if err != nil {
		return false
	}
	w.outOfTime() // Account for a slow probe, including an empty directory.

	_, m := w.matcherFor(dir, segs, patterns, rules)

	probe := make([]string, len(segs)+1)
	copy(probe, segs)
	for _, e := range entries {
		probe[len(segs)] = e.Name()
		if w.include(e.Name(), w.probeIsDir(dir, e), probe, m) {
			return true
		}
	}
	return false
}

// probeIsDir is classify's answer to "is it a directory" alone, which is all
// include needs: whether a link loops or is refused changes how it is shown,
// never whether it is.
func (w *walker) probeIsDir(dir string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink == 0 {
		return e.IsDir()
	}
	info, err := w.fs.Stat(filepath.Join(dir, e.Name()))
	return err == nil && info.IsDir()
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
