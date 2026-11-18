// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
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

// DefaultForgeAppMaxEntries bounds the entries one FindForgeApps visits. A
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
	// Truncated is true when the walk stopped before covering the
	// repository (its entry budget, the app cap, its deadline or its
	// context): Apps is then what it found until then, never the whole
	// answer. TruncatedReason finishes "the search stopped early because".
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
}

// FindForgeApps walks the repository at repoRoot for forge.yaml files and
// returns one ForgeApp per forge project manifest in it.
//
// The walk is bounded the way Discover's is (ForgeAppOptions; a bound that
// stops it is reported, never hidden), never follows a symlink, and never
// enters a directory skipDir or forgeAppSkipDirs names, nor a nested git
// checkout: a repository inside this one is its own repository, whose apps
// are reported on it.
//
// The forge.yaml at the repository root counts when it names something,
// exactly as projects.is_forge has always read it. One deeper down counts only
// when it is a forge manifest (forgeManifest): forge.yaml is a common enough
// file name that a workflow preset or a fixture can be called that too.
//
// An unreadable or missing root is an error; an unreadable subdirectory or
// manifest is skipped.
func FindForgeApps(ctx context.Context, repoRoot string, opts ForgeAppOptions) (ForgeAppScan, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = ForgeAppMaxDepth
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultForgeAppMaxEntries
	}

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

	var (
		apps    []ForgeApp
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
			if os.IsPermission(walkErr) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return walkErr
		}

		if d.IsDir() {
			if path == abs {
				return nil
			}
			if strings.Count(path, string(filepath.Separator))-rootDepth > maxDepth {
				return fs.SkipDir
			}
			if skipForgeAppDir(d.Name()) || isGitDir(path) {
				return fs.SkipDir
			}
			return nil
		}

		// A regular file named forge.yaml; a symlink to one is not followed.
		if d.Name() != "forge.yaml" || !d.Type().IsRegular() {
			return nil
		}
		dir := filepath.Dir(path)
		isRoot := dir == abs
		name, ok := readForgeManifest(path, !isRoot)
		if !ok {
			return nil
		}
		subpath := ""
		if !isRoot {
			rel, err := filepath.Rel(abs, dir)
			if err != nil {
				return nil
			}
			subpath = filepath.ToSlash(rel)
		}
		if len(apps) >= MaxForgeAppsPerRepo {
			stopped = fmt.Sprintf("it found %d forge apps", MaxForgeAppsPerRepo)
			return fs.SkipAll
		}
		apps = append(apps, ForgeApp{Subpath: subpath, Name: name})
		return nil
	})
	if walkErr != nil {
		return ForgeAppScan{}, fmt.Errorf("scan repository for forge apps: %w", walkErr)
	}

	sort.Slice(apps, func(i, j int) bool { return apps[i].Subpath < apps[j].Subpath })
	out := ForgeAppScan{Apps: apps}
	if stopped != "" {
		out.Truncated = true
		out.TruncatedReason = stopped
	}
	return out, nil
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

// forgeManifestMaxBytes bounds a forge.yaml read. A real manifest is a few KB.
const forgeManifestMaxBytes = 1 << 20

// readForgeManifest returns the trimmed top-level `name` of the forge.yaml at
// path, and whether it is a forge app: it names something and, when
// requireManifest is set, is a forge manifest (forgeManifest). A missing,
// oversized or unparseable file is not one.
func readForgeManifest(path string, requireManifest bool) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, forgeManifestMaxBytes+1))
	if err != nil || len(data) > forgeManifestMaxBytes {
		return "", false
	}
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return "", false
	}
	name, _ := top["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if requireManifest && !forgeManifest(top, filepath.Dir(path)) {
		return "", false
	}
	return name, true
}

// forgeManifest reports whether a parsed forge.yaml in dir is a forge
// project's manifest rather than another file of that name: it pins forge
// (`forge_version`, which every project forge scaffolds carries), declares a
// Go module (`module_path`), or sits beside the deploy/kcl tree a forge
// project's environments live in.
func forgeManifest(top map[string]any, dir string) bool {
	if _, ok := top["forge_version"]; ok {
		return true
	}
	if _, ok := top["module_path"]; ok {
		return true
	}
	info, err := os.Stat(filepath.Join(dir, "deploy", "kcl"))
	return err == nil && info.IsDir()
}
