// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EVERY FORGE APP IN A REPOSITORY, NOT ONLY THE ONE AT ITS ROOT.
//
// A project's forge identity used to be one name: the forge.yaml at the
// project root (projects.forge_project_name). A repository that holds several
// forge apps (a monorepo with a site, an API and a worker), or a project of
// several repositories each with its own forge.yaml (this workspace:
// control-plane, forge, reliant), mapped to at most one of them. FindForgeApps
// is the code half of the code <-> app link (control-plane
// docs/design/unified-shell-and-projects.md §8.1, D3): a bounded walk of one
// repository that reports every forge app in it, by name and directory.

// ForgeAppMaxDepth is how deep below a repository's root FindForgeApps looks
// for a forge.yaml: the root is depth 0, so an app at a/b/c/d/forge.yaml is
// the deepest found.
const ForgeAppMaxDepth = 4

// DefaultForgeAppMaxEntries bounds the directory entries one FindForgeApps
// lists. A
// repository to depth 4, with dependency and build trees skipped, is a few
// thousand; past this the walk stops and says so.
const DefaultForgeAppMaxEntries = 20_000

// MaxForgeAppsPerRepo bounds how many apps one repository reports. A real
// monorepo has a handful; a tree of hundreds is fixtures, and recording them
// would show every one as an app.
const MaxForgeAppsPerRepo = 64

// forgeAppSkipDirs are skipped by the forge-app walk on top of skipDir's
// (hidden directories, node_modules, vendor, build output, ...): generated
// code and test fixtures, where a forge.yaml is an input to a test, not an
// app.
var forgeAppSkipDirs = map[string]struct{}{
	"gen":      {},
	"testdata": {},
}

// ForgeApp is one forge app found in a repository.
type ForgeApp struct {
	// Subpath is the directory holding the forge.yaml, relative to the
	// repository root and "/"-separated whatever the machine's OS; "" is the
	// root itself.
	Subpath string
	// Name is forge.yaml's `name`: the app's name in the project's org.
	Name string
}

// ForgeAppScan is what one FindForgeApps found.
type ForgeAppScan struct {
	// Apps is ordered by Subpath.
	Apps []ForgeApp
	// Truncated is true when the scan did not cover the whole repository:
	// a bound stopped it (its entry budget, the app cap, its deadline or its
	// context), or a directory or manifest in it could not be read. Apps is
	// then what it found, never the whole answer: an app it could not see
	// may still be there. TruncatedReason says why, finishing "the list may
	// be incomplete because".
	Truncated       bool
	TruncatedReason string
}

// ForgeAppOptions bounds a FindForgeApps. Zero values take the defaults.
type ForgeAppOptions struct {
	// MaxDepth is how deep to look (ForgeAppMaxDepth when <= 0).
	MaxDepth int
	// MaxEntries is the entry budget (DefaultForgeAppMaxEntries when <= 0).
	MaxEntries int
	// Deadline, when set, stops the walk at that time with what it has.
	Deadline time.Time
	// FS, when set, is the repository's tree, rooted at the repository;
	// repoRoot then only names it. nil reads the OS filesystem at repoRoot.
	// It is the seam a test injects read failures through.
	FS fs.FS
}

// FindForgeApps walks the repository at repoRoot for forge.yaml files and
// returns one ForgeApp per forge project manifest in it.
//
// The walk is bounded the way Discover's is (ForgeAppOptions; a bound that
// stops it is reported, never hidden), within one directory as well as across
// them: a directory is listed in batches, its entries charged to the budget
// as they arrive (forgeAppWalk), so one very wide directory cannot stall it.
// It never follows a symlink, and never
// enters a directory skipDir or forgeAppSkipDirs names, nor a nested git
// checkout: a repository inside this one is its own repository, whose apps
// are reported on it.
//
// The forge.yaml at the repository root counts when it names something,
// exactly as projects.is_forge has always read it. One deeper down counts only
// when it is a forge manifest (forgeManifest): forge.yaml is a common enough
// file name that a workflow preset or a fixture can be called that too.
//
// An unreadable or missing root is an error. A directory or manifest below it
// that cannot be read is NOT "no app there": the walk carries on and the scan
// is reported Truncated, so its caller keeps what it knew about that part of
// the tree rather than recording it as gone. A manifest that reads fine but is
// not one (no name, not YAML, too large, not a forge manifest) is simply not
// an app.
func FindForgeApps(ctx context.Context, repoRoot string, opts ForgeAppOptions) (ForgeAppScan, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = ForgeAppMaxDepth
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultForgeAppMaxEntries
	}

	fsys := opts.FS
	if fsys == nil {
		abs, err := filepath.Abs(repoRoot)
		if err != nil {
			return ForgeAppScan{}, fmt.Errorf("resolve repository path: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return ForgeAppScan{}, fmt.Errorf("repository path: %w", err)
		}
		if !info.IsDir() {
			return ForgeAppScan{}, fmt.Errorf("repository path is not a directory: %s", abs)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		// os.DirFS lists entries with their own (Lstat) type, so a
		// symlink is never a directory to the walk and is not followed.
		fsys = os.DirFS(abs)
	}

	w := &forgeAppWalk{
		ctx:        ctx,
		fsys:       fsys,
		maxDepth:   maxDepth,
		maxEntries: maxEntries,
		deadline:   opts.Deadline,
	}
	if err := w.walk(".", 0); err != nil && !errors.Is(err, errForgeAppWalkStopped) {
		return ForgeAppScan{}, fmt.Errorf("scan repository for forge apps: %w", err)
	}

	apps := w.apps
	sort.Slice(apps, func(i, j int) bool { return apps[i].Subpath < apps[j].Subpath })
	out := ForgeAppScan{Apps: apps}
	switch {
	case w.stopped != "":
		out.Truncated = true
		out.TruncatedReason = w.stopped
	case len(w.unreadable) > 0:
		out.Truncated = true
		out.TruncatedReason = fmt.Sprintf("it could not read %s", w.unreadable[0])
		if n := len(w.unreadable) - 1; n > 0 {
			out.TruncatedReason += fmt.Sprintf(" and %d more", n)
		}
	}
	return out, nil
}

// forgeAppReadBatch is the most entries the walk asks a directory for at
// once. The walk checks its context, deadline and entry budget between
// batches, so a directory of a million entries costs at most one batch past
// a bound, not a read and sort of the whole listing first (what fs.WalkDir
// does before its callback sees any of it).
const forgeAppReadBatch = 256

// errForgeAppWalkStopped ends a walk a bound stopped; forgeAppWalk.stopped
// says which.
var errForgeAppWalkStopped = errors.New("forge-app walk stopped")

// forgeAppWalk is one FindForgeApps walk: a depth-first walk of fsys in
// lexical order, as fs.WalkDir's, whose directory listings are bounded too.
//
// Every entry a listing returns is charged to the budget as it arrives, and a
// directory is walked only once it is listed whole, then in name order. So
// which entries a walk looks at, and where the budget stops it, depend only
// on the tree, never on the order the filesystem lists a directory in: the
// same tree gives the same scan, truncated or not (its deadline and context
// aside, which are time).
type forgeAppWalk struct {
	ctx        context.Context
	fsys       fs.FS
	maxDepth   int
	maxEntries int
	deadline   time.Time

	entries    int
	apps       []ForgeApp
	stopped    string
	unreadable []string
}

// bounded reports whether a bound has stopped the walk, recording which.
func (w *forgeAppWalk) bounded() bool {
	if w.stopped != "" {
		return true
	}
	if w.ctx.Err() != nil {
		w.stopped = "the request ended"
		return true
	}
	if !w.deadline.IsZero() && !time.Now().Before(w.deadline) {
		w.stopped = "it ran out of time"
		return true
	}
	return false
}

// couldNotRead records p as unread. Unread is unknown, whatever the error:
// the subtree may hold an app.
func (w *forgeAppWalk) couldNotRead(p string) { w.unreadable = append(w.unreadable, p) }

// walk visits dir, at depth below the repository root (which is depth 0).
// It returns errForgeAppWalkStopped once a bound stops the walk, and the
// error when the root itself cannot be listed; a deeper directory that
// cannot be is recorded unread and walked past.
func (w *forgeAppWalk) walk(dir string, depth int) error {
	list, err := w.readDir(dir)
	if errors.Is(err, errForgeAppWalkStopped) {
		return err
	}
	if err != nil {
		if dir == "." {
			return err
		}
		w.couldNotRead(dir)
		return nil
	}

	for _, d := range list {
		if w.bounded() {
			return errForgeAppWalkStopped
		}
		p := path.Join(dir, d.Name())
		if d.IsDir() {
			if depth+1 > w.maxDepth || skipForgeAppDir(d.Name()) {
				continue
			}
			nested, err := hasGitEntry(w.fsys, p)
			if err != nil {
				// Whether it is a nested checkout cannot be told, so
				// neither can whose apps are in it.
				w.couldNotRead(path.Join(p, ".git"))
				continue
			}
			if nested {
				continue
			}
			if err := w.walk(p, depth+1); err != nil {
				return err
			}
			continue
		}

		// A regular file named forge.yaml; a symlink to one is not followed.
		if d.Name() != "forge.yaml" || !d.Type().IsRegular() {
			continue
		}
		isRoot := dir == "."
		name, ok, err := readForgeManifest(w.fsys, p, !isRoot)
		if err != nil {
			w.couldNotRead(p)
			continue
		}
		if !ok {
			continue
		}
		subpath := ""
		if !isRoot {
			subpath = dir
		}
		if len(w.apps) >= MaxForgeAppsPerRepo {
			w.stopped = fmt.Sprintf("it found %d forge apps", MaxForgeAppsPerRepo)
			return errForgeAppWalkStopped
		}
		w.apps = append(w.apps, ForgeApp{Subpath: subpath, Name: name})
	}
	return nil
}

// readDir lists dir sorted by name, as fs.ReadDir does, but reads it in
// batches of at most forgeAppReadBatch entries, each charged to the budget,
// and checks the walk's bounds before every batch. It never holds more than
// one entry past the budget. errForgeAppWalkStopped: a bound stopped it
// part-way, and the partial listing is dropped.
func (w *forgeAppWalk) readDir(dir string) ([]fs.DirEntry, error) {
	if w.bounded() {
		return nil, errForgeAppWalkStopped
	}
	f, err := w.fsys.Open(dir)
	if err != nil {
		return nil, err
	}
	// Read-only: a failed close loses nothing the listing did not already have.
	defer func() { _ = f.Close() }()
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: errors.New("not implemented")}
	}

	var list []fs.DirEntry
	for {
		if w.bounded() {
			return nil, errForgeAppWalkStopped
		}
		// One past what the budget has left is enough to know it is spent.
		batch, err := rd.ReadDir(min(forgeAppReadBatch, w.maxEntries-w.entries+1))
		w.entries += len(batch)
		if w.entries > w.maxEntries {
			w.stopped = fmt.Sprintf("it looked at %d entries", w.maxEntries)
			return nil, errForgeAppWalkStopped
		}
		list = append(list, batch...)
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	return list, nil
}

// skipForgeAppDir reports whether the forge-app walk stays out of a directory
// named name.
func skipForgeAppDir(name string) bool {
	if skipDir(name) {
		return true
	}
	_, skip := forgeAppSkipDirs[name]
	return skip
}

// hasGitEntry reports whether dir in fsys holds a `.git` entry (a checkout,
// or a worktree linked to one). An error other than "no such entry" is
// returned: it does not say there is none.
func hasGitEntry(fsys fs.FS, dir string) (bool, error) {
	_, err := fs.Stat(fsys, path.Join(dir, ".git"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// forgeManifestMaxBytes bounds a forge.yaml read. A real manifest is a few KB.
const forgeManifestMaxBytes = 1 << 20

// readForgeManifest returns the trimmed top-level `name` of the forge.yaml at
// p in fsys, and whether it is a forge app: it names something and, when
// requireManifest is set, is a forge manifest (forgeManifest).
//
// Not one (ok false, err nil): the file is gone, oversized, not YAML, or names
// nothing. Unknown (err non-nil): it, or what forgeManifest looks at beside
// it, could not be read.
func readForgeManifest(fsys fs.FS, p string, requireManifest bool) (string, bool, error) {
	f, err := fsys.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// Read-only: a failed close loses nothing the read did not already have.
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, forgeManifestMaxBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > forgeManifestMaxBytes {
		return "", false, nil
	}
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return "", false, nil
	}
	name, _ := top["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, nil
	}
	if requireManifest {
		isManifest, err := forgeManifest(fsys, top, path.Dir(p))
		if err != nil || !isManifest {
			return "", false, err
		}
	}
	return name, true, nil
}

// forgeManifest reports whether a parsed forge.yaml in dir is a forge
// project's manifest rather than another file of that name: it pins forge
// (`forge_version`, which every project forge scaffolds carries), declares a
// Go module (`module_path`), or sits beside the deploy/kcl tree a forge
// project's environments live in. An error is a deploy/kcl that could not be
// looked at.
func forgeManifest(fsys fs.FS, top map[string]any, dir string) (bool, error) {
	if _, ok := top["forge_version"]; ok {
		return true, nil
	}
	if _, ok := top["module_path"]; ok {
		return true, nil
	}
	info, err := fs.Stat(fsys, path.Join(dir, "deploy", "kcl"))
	switch {
	case err == nil:
		return info.IsDir(), nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
