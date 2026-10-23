// Package codemod holds forge's deterministic rewrites of a project's OWN
// files: the mechanical half of a release that changed a shape the project
// wrote down. Each codemod is keyed to the release that broke the shape — the
// same key its migration skill (skills/forge/migrations/<version>) carries —
// and is reachable two ways:
//
//   - `forge fix <name>` runs one on request (`--dry-run` lists what it would
//     rewrite);
//   - `forge project upgrade` runs every codemod marked Upgrade whose release
//     the project's baseline predates, before it touches a template.
//
// A codemod is idempotent and decides from the tree, never from a version: on
// a project that does not have the old shape it rewrites nothing and reports
// nothing. That is what lets upgrade run it on every crossing, and what makes
// a second run a no-op.
//
// The intent-bearing half of a migration — the choices only a person or an
// agent can make — stays prose, in the migration skill. A codemod that cannot
// decide something says so in its report rather than guessing.
//
//forge:exclude-contract: a registry of source rewrites over the local tree — data and functions, no collaborator to substitute
package codemod

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Report is what one codemod run did, or (dry run) would do.
type Report struct {
	// Rewrites are the mechanical rewrites, one human-readable line each
	// ("internal/app/lifecycle.go: lifecyclekit.Run → operatorkit.Run"),
	// in the order they were made.
	Rewrites []string
	// Next is what the codemod could not decide, as the exact edits left to
	// make, in order. Empty when the rewrite is the whole job.
	Next []string
	// Blocked are the shapes that stop a restructuring codemod from
	// writing anything — each with its file:line and the edit that clears
	// it. A codemod that reports any rewrites nothing: a conversion that
	// compiles but silently changes what a process does is worse than none.
	Blocked []string
	// Verify are the commands that prove the result. Empty means the
	// default: go build, forge generate, forge lint.
	Verify []string
}

// Changed reports whether the run rewrote (or would rewrite) anything.
func (r Report) Changed() bool { return len(r.Rewrites) > 0 }

// Codemod is one deterministic rewrite.
type Codemod struct {
	// Name is the `forge fix <name>` subcommand.
	Name string
	// Version is the release that broke the shape this rewrites. It names
	// the migration skill that tells the rest of the story
	// (skills/forge/migrations/<Version>).
	Version string
	// Short is the one-line summary `forge fix --help` prints.
	Short string
	// Long is the subcommand's help body.
	Long string
	// Upgrade marks a codemod `forge project upgrade` runs on its own when
	// the project's baseline predates Version. Only a rewrite that keeps the
	// program's meaning exactly — a rename, a moved symbol — qualifies; a
	// restructuring is run on request, after the person running it has read
	// what it does.
	Upgrade bool
	// CleanTree marks a codemod that replaces files wholesale: `forge fix`
	// refuses to run it over uncommitted changes (unless --allow-dirty), so
	// `git diff` is the review and `git checkout .` the undo.
	CleanTree bool
	// Scoped marks a codemod that honours Options.Scope: `forge fix <name>
	// --scope <path>` rewrites only the files under the paths named, while
	// whatever it analyses to decide a rewrite still reads the whole
	// project. A codemod whose rewrite is one decision over the whole tree
	// (a restructuring) leaves it false, and `forge fix` offers no --scope.
	Scoped bool
	// Run rewrites the project at dir, as opts asks.
	Run func(dir string, opts Options) (Report, error)
}

// Options is how one codemod run is asked to behave.
type Options struct {
	// Write false changes nothing: the run reports what it would rewrite.
	Write bool
	// Scope restricts the files the run may rewrite (and reports on) to
	// those under its paths. Nil is the whole project. Only a Scoped
	// codemod is handed one.
	Scope *Scope
}

// Scope is a set of project-relative paths — directories or files — that
// bounds the files a run rewrites. Parallel lanes in one checkout each run a
// codemod over their own slice, as `forge lint --scope` lints one. A nil
// *Scope is the whole project, and every method is nil-safe.
type Scope struct {
	paths []string // slash-separated, cleaned, sorted
}

// ParseScope validates raw scope paths against the project at root. Each
// must exist and lie inside the project: a typo would otherwise scope the
// run to nothing and report it clean.
func ParseScope(root string, raw []string) (*Scope, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	s := &Scope{}
	seen := map[string]bool{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs := r
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%q is outside the project root %s", r, root)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("%q does not exist: a scope that matches nothing would report a clean run over no files", r)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil, errors.New("--scope . is the whole project: drop --scope")
		}
		if !seen[rel] {
			seen[rel] = true
			s.paths = append(s.paths, rel)
		}
	}
	if len(s.paths) == 0 {
		return nil, errors.New("--scope was given no path")
	}
	sort.Strings(s.paths)
	return s, nil
}

// Contains reports whether rel — a project-relative path — lies under the
// scope. Everything does under a nil scope.
func (s *Scope) Contains(rel string) bool {
	if s == nil {
		return true
	}
	f := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./")
	for _, p := range s.paths {
		if f == p || strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// Paths returns the scope's project-relative paths, none for the whole
// project.
func (s *Scope) Paths() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.paths...)
}

// String lists the scope's paths, "" for the whole project.
func (s *Scope) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(s.paths, ", ")
}

var registry []Codemod

// register adds a codemod. Called from each codemod file's init.
func register(c Codemod) {
	for _, have := range registry {
		if have.Name == c.Name {
			panic(fmt.Sprintf("codemod %q registered twice", c.Name))
		}
	}
	registry = append(registry, c)
}

// All returns every codemod, by release and then name.
func All() []Codemod {
	out := append([]Codemod(nil), registry...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Lookup returns the codemod named name.
func Lookup(name string) (Codemod, bool) {
	for _, c := range registry {
		if c.Name == name {
			return c, true
		}
	}
	return Codemod{}, false
}

// skipDir reports whether a directory holds no Go source of the project's
// own: generated code, dependencies, tool state and test fixtures (the go
// tool ignores testdata, so a rewrite there would change nothing that
// builds).
func skipDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "testdata", "gen":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// generatedMarker is the line `go generate`-style tooling stamps on a file
// nobody should edit (https://go.dev/s/generatedcode).
const generatedMarker = "// Code generated "

// isGenerated reports whether src is a generated Go file: forge regenerates
// it, so a rewrite of it would be overwritten — the generator has to change
// instead.
func isGenerated(src []byte) bool {
	for _, line := range strings.SplitN(string(src), "\n", 40) {
		if strings.HasPrefix(line, generatedMarker) && strings.HasSuffix(strings.TrimSpace(line), "DO NOT EDIT.") {
			return true
		}
		if strings.HasPrefix(line, "package ") {
			return false
		}
	}
	return false
}

// ownedGoFiles walks the project's own Go files — not generated, not in a
// dependency or fixture tree — calling fn with the path relative to dir and
// the file's bytes. Paths are visited in lexical order.
func ownedGoFiles(dir string, fn func(rel string, src []byte) error) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if path != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			// A nested module (its own go.mod) is another project.
			if path != dir {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_gen.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isGenerated(src) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), src)
	})
}

// writeFile replaces path's content atomically, keeping its mode.
func writeFile(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, content, info.Mode().Perm())
}
