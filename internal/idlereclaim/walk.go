// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// KeepFile is the marker that keeps a scratch directory whole: a file (or
// directory) of this name at its top.
const KeepFile = ".keep"

// maxScanEntries bounds one directory's walk. A directory too large to walk
// whole is unmeasured, and an unmeasured directory is held.
const maxScanEntries = 500_000

// buildOutputs are the directory names that are a JavaScript build's output
// when they sit beside a package.json; "target" is Cargo's beside a
// Cargo.toml. A node_modules is an install's output wherever it is. A name on
// neither list is never removed by the strip, and neither is one of these
// beside no manifest: an agent's "out/" of briefings is not a build.
var buildOutputs = map[string]string{
	"dist":          "package.json",
	"out":           "package.json",
	"build":         "package.json",
	".next":         "package.json",
	".turbo":        "package.json",
	".vite":         "package.json",
	".svelte-kit":   "package.json",
	".nuxt":         "package.json",
	".parcel-cache": "package.json",
	"target":        "Cargo.toml",
}

// gitRoot is a git working tree found inside a scanned directory: a
// repository (a .git directory) or a linked worktree (a .git file).
type gitRoot struct {
	Path   string
	Linked bool
}

// dirScan is what one walk of a directory found.
type dirScan struct {
	// Newest is the newest modification time of any entry, the directory
	// itself included. A node_modules counts by its own directory's time
	// (an install or a removal in it changes that), and is not descended.
	Newest time.Time
	// Keep is whether KeepFile sits at the top.
	Keep bool
	// Truncated is whether the walk stopped at maxScanEntries.
	Truncated bool
	// Unreadable names the first entry the walk could not read.
	Unreadable string
	// Mount names the first entry on another filesystem than the top.
	Mount string
	// Targets are the regenerable directories found (absolute), outermost
	// only, sorted.
	Targets []string
	// GitRoots are the git working trees found, sorted.
	GitRoots []gitRoot
}

// unmeasured is why a scan cannot vouch for the directory, or "".
func (s dirScan) unmeasured() string {
	switch {
	case s.Truncated:
		return "too large to inspect completely"
	case s.Unreadable != "":
		return "cannot read " + s.Unreadable
	case s.Mount != "":
		return "holds another filesystem at " + s.Mount
	}
	return ""
}

// scanDir walks dir once. It never follows a symlink, never crosses into
// another filesystem, and never descends a node_modules or a .git directory.
func scanDir(ctx context.Context, dir string) dirScan {
	var s dirScan
	top, err := os.Lstat(dir)
	if err != nil || !top.IsDir() {
		s.Unreadable = "."
		return s
	}
	topDev, devKnown := deviceOf(top)
	if _, err := os.Lstat(filepath.Join(dir, KeepFile)); err == nil {
		s.Keep = true
	}
	entries := 0
	inTarget := "" // the build-output directory being walked, for Newest only
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		rel := relOf(dir, p)
		if err != nil {
			if s.Unreadable == "" {
				s.Unreadable = rel
			}
			return fs.SkipAll
		}
		entries++
		if entries > maxScanEntries {
			s.Truncated = true
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil // gone since the directory was read
			}
			if s.Unreadable == "" {
				s.Unreadable = rel
			}
			return fs.SkipAll
		}
		if info.ModTime().After(s.Newest) {
			s.Newest = info.ModTime()
		}
		if !d.IsDir() {
			if d.Name() == ".git" && info.Mode().IsRegular() {
				s.GitRoots = append(s.GitRoots, gitRoot{Path: filepath.Dir(p), Linked: true})
			}
			return nil
		}
		if devKnown {
			if dev, ok := deviceOf(info); !ok || dev != topDev {
				if s.Mount == "" {
					s.Mount = rel
				}
				return fs.SkipDir
			}
		}
		if p == dir {
			return nil
		}
		if inTarget != "" && !strings.HasPrefix(p, inTarget+string(filepath.Separator)) {
			inTarget = ""
		}
		switch name := d.Name(); {
		case name == ".git":
			s.GitRoots = append(s.GitRoots, gitRoot{Path: filepath.Dir(p)})
			return fs.SkipDir
		case name == "node_modules":
			if inTarget == "" {
				s.Targets = append(s.Targets, p)
			}
			return fs.SkipDir
		case inTarget == "" && isBuildOutput(p, name):
			s.Targets = append(s.Targets, p)
			inTarget = p
		}
		return nil
	})
	if ctx.Err() != nil && s.Unreadable == "" {
		s.Unreadable = "." // stopped: not a complete measure
	}
	sort.Strings(s.Targets)
	sort.Slice(s.GitRoots, func(i, j int) bool { return s.GitRoots[i].Path < s.GitRoots[j].Path })
	return s
}

// isBuildOutput reports whether the directory p, named name, is a build's
// output by the manifest beside it.
func isBuildOutput(p, name string) bool {
	manifest, ok := buildOutputs[name]
	if !ok {
		return false
	}
	info, err := os.Lstat(filepath.Join(filepath.Dir(p), manifest))
	return err == nil && info.Mode().IsRegular()
}

// enclosingGitRoot is the innermost git root that p is inside, or nil.
func enclosingGitRoot(roots []gitRoot, p string) *gitRoot {
	var best *gitRoot
	for i := range roots {
		r := &roots[i]
		if within(r.Path, p) && (best == nil || len(r.Path) > len(best.Path)) {
			best = r
		}
	}
	return best
}

// within reports whether p is root or under it.
func within(root, p string) bool {
	root, p = filepath.Clean(root), filepath.Clean(p)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func relOf(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}
