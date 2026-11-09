package cigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Affected-package mode: on a branch, the gate's unit lane tests only the Go
// packages a change can reach. A whole-module `-race -count=1` run was measured
// at ~14 min on a shared machine, and a typical branch touches a handful of
// packages; testing the rest re-proves what the default branch already
// proved.
//
// A package P is tested when its test binary — P, its in-package and external
// tests, and everything they import, transitively — contains a changed
// package. "Changed" is decided from the files that differ between the work
// tree and `git merge-base HEAD origin/<default>`:
//
//   - a .go file maps to the package in its directory;
//   - a file a package embeds (go:embed, including test embeds) maps to that
//     package, so a template or fixture compiled into it counts;
//   - a file under a package's own testdata/ maps to that package.
//
// ANY other changed file falls back to the full suite, because nothing proves
// which tests read it: go.mod / go.sum, a testdata/ outside a package, KCL,
// proto, migrations, the Taskfile, scripts, a README a test happens to read —
// unless forge.yaml's ci.gate.affected_ignore names it. So does the default
// branch itself, a branch with no changes, a missing origin/<default>, and an
// explicit request (CI_FULL=1). The mode and its reason are always printed.

// FullEnv forces the full unit suite on a branch.
const FullEnv = "CI_FULL"

// BaseEnv names the ref a branch's changes are measured from, for a branch
// stacked on another (default: origin/<default branch>).
const BaseEnv = "CI_BASE"

// Affected is the unit lane's mode for one gate run.
type Affected struct {
	// Full means run everything; Reason says why.
	Full   bool
	Reason string
	// Packages are the import paths to test (affected mode).
	Packages []string
	// Total is how many packages in the module have tests.
	Total int
	// BaseRef is the ref diffed against (origin/main); Base its merge-base
	// with HEAD, short.
	BaseRef, Base string
	// Changed are the changed files, project-relative.
	Changed []string
}

// Line is the one line the gate prints about the unit lane's mode.
func (a Affected) Line() string {
	if a.Full {
		return "unit tests: full suite — " + a.Reason
	}
	if len(a.Packages) == 0 {
		return fmt.Sprintf("unit tests: affected mode — no package with tests reaches the %d changed file(s) since %s (%s); %s=1 runs the full suite",
			len(a.Changed), a.BaseRef, a.Base, FullEnv)
	}
	return fmt.Sprintf("unit tests: affected mode — %d of %d test packages reach the %d changed file(s) since %s (%s); %s=1 runs the full suite",
		len(a.Packages), a.Total, len(a.Changed), a.BaseRef, a.Base, FullEnv)
}

// AffectedInput configures ComputeAffected.
type AffectedInput struct {
	Root string
	// Env is the environment go list runs with; nil is the process's.
	Env []string
	// Force, when non-empty, is the reason to run the full suite regardless
	// (CI_FULL=1, --full).
	Force string
	// BaseRef overrides origin/<default>: the ref a stacked branch diffs
	// against (CI_BASE / --base).
	BaseRef string
	// Ignore are ci.gate.affected_ignore's globs: files no Go test reads.
	Ignore []string
	// Git and GoList run the two tools; nil uses the real binaries. Tests
	// substitute.
	Git    func(ctx context.Context, dir string, args ...string) (string, error)
	GoList func(ctx context.Context, dir string, env []string) (io.Reader, error)
}

// ComputeAffected decides the unit lane's mode for the work tree at in.Root.
// It never fails: anything it cannot decide is a reason to run everything.
func ComputeAffected(ctx context.Context, in AffectedInput) Affected {
	full := func(reason string) Affected { return Affected{Full: true, Reason: reason} }
	if in.Force != "" {
		return full(in.Force)
	}
	git := in.Git
	if git == nil {
		git = runGit
	}
	baseRef := in.BaseRef
	defaultBranch := ""
	if baseRef == "" {
		if ref, err := git(ctx, in.Root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
			baseRef = strings.TrimPrefix(ref, "refs/remotes/")
		} else {
			for _, cand := range []string{"origin/main", "origin/master"} {
				if _, err := git(ctx, in.Root, "rev-parse", "--verify", "--quiet", "refs/remotes/"+cand); err == nil {
					baseRef = cand
					break
				}
			}
		}
		if baseRef == "" {
			return full("no origin/<default branch> to diff against")
		}
		defaultBranch = strings.TrimPrefix(baseRef, "origin/")
	}
	if branch, err := git(ctx, in.Root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && defaultBranch != "" && branch == defaultBranch {
		return full("on the default branch (" + branch + ")")
	}
	base, err := git(ctx, in.Root, "merge-base", "HEAD", baseRef)
	if err != nil || base == "" {
		return full("no merge-base with " + baseRef)
	}
	diff, err := git(ctx, in.Root, "diff", "--name-only", "--no-renames", base)
	if err != nil {
		return full("cannot diff against " + baseRef)
	}
	untracked, err := git(ctx, in.Root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return full("cannot list untracked files")
	}
	changed := uniqueLines(diff + "\n" + untracked)
	short := base
	if len(short) > 12 {
		short = short[:12]
	}
	if len(changed) == 0 {
		// Nothing to narrow by: a branch identical to its base proves
		// nothing about the base unless the suite actually runs.
		return full("nothing changed since " + baseRef + " (" + short + ")")
	}
	a := Affected{BaseRef: baseRef, Base: short, Changed: changed}

	list := in.GoList
	if list == nil {
		list = runGoList
	}
	r, err := list(ctx, in.Root, in.Env)
	if err != nil {
		return full("go list failed: " + firstLine(err.Error()))
	}
	g, err := readPackageGraph(r, in.Root)
	if err != nil {
		return full("cannot read go list output: " + err.Error())
	}
	changedPkgs := map[string]bool{}
	for _, f := range changed {
		if ignoredByGlobs(f, in.Ignore) {
			continue
		}
		pkg, ok := g.owner(f)
		if !ok {
			return full(fmt.Sprintf("%s changed, and it is not Go source, an embedded file or testdata of a package — nothing proves which tests read it (ci.gate.affected_ignore lists files none do)", f))
		}
		changedPkgs[pkg] = true
	}
	a.Total = len(g.testDeps)
	for pkg, deps := range g.testDeps {
		if changedPkgs[pkg] {
			a.Packages = append(a.Packages, pkg)
			continue
		}
		for _, d := range deps {
			if changedPkgs[d] {
				a.Packages = append(a.Packages, pkg)
				break
			}
		}
	}
	sort.Strings(a.Packages)
	return a
}

// ApplyAffected narrows the unit-tests row to a's packages. In full mode it
// only annotates the row.
func ApplyAffected(checks []Check, a Affected) []Check {
	out := append([]Check(nil), checks...)
	for i := range out {
		c := &out[i]
		if c.Name != UnitTests || c.NotApplicable != "" {
			continue
		}
		switch {
		case a.Full:
			c.Note = "full suite: " + a.Reason
		case len(a.Packages) == 0:
			c.NotApplicable = fmt.Sprintf("affected mode: no package with tests reaches the changes since %s (%s=1 runs the full suite)", a.BaseRef, FullEnv)
		default:
			// `task test -- <packages>` replaces the default ./... with the
			// list. It also skips the frontend lane, which is right: affected
			// mode means every changed file is Go.
			c.Argv = append(append(append([]string(nil), c.Argv...), "--"), a.Packages...)
			c.Note = fmt.Sprintf("affected mode: %d of %d packages (since %s)", len(a.Packages), a.Total, a.BaseRef)
		}
	}
	return out
}

// packageGraph is what affected mode needs from `go list -deps -test`.
type packageGraph struct {
	root string
	// dirPkg maps a package directory (absolute) to its import path.
	dirPkg map[string]string
	// embedPkg maps an embedded file (absolute) to the package embedding it.
	embedPkg map[string]string
	// testDeps maps each module package that has tests to every package its
	// test binary links.
	testDeps map[string][]string
}

// listedPackage is the part of a `go list -json` package affected mode reads;
// encoding/json fills it.
type listedPackage struct {
	ImportPath      string   `json:"ImportPath"`
	Name            string   `json:"Name"`
	Dir             string   `json:"Dir"`
	ForTest         string   `json:"ForTest"`
	Standard        bool     `json:"Standard"`
	Deps            []string `json:"Deps"`
	EmbedFiles      []string `json:"EmbedFiles"`
	TestEmbedFiles  []string `json:"TestEmbedFiles"`
	XTestEmbedFiles []string `json:"XTestEmbedFiles"`
}

func readPackageGraph(r io.Reader, root string) (*packageGraph, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// go list reports each Dir as the go command saw its working
	// directory, which is the symlink-resolved path whenever $PWD does not
	// name it (an explicit Env carries the caller's PWD, not root): on macOS
	// t.TempDir's /var/folders/... comes back as /private/var/folders/...,
	// as does any project under a symlinked directory. Every in-module
	// package then looked foreign, and every changed .go file ran the full
	// suite. Dirs under the resolved root are mapped back onto root, the
	// spelling owner() joins changed files against.
	realRoot := absRoot
	if r, rerr := filepath.EvalSymlinks(absRoot); rerr == nil {
		realRoot = r
	}
	g := &packageGraph{root: absRoot, dirPkg: map[string]string{}, embedPkg: map[string]string{}, testDeps: map[string][]string{}}
	dec := json.NewDecoder(r)
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if p.Standard || p.Dir == "" {
			continue
		}
		// The test binary's main package: "<pkg>.test", linking everything
		// the package's tests need.
		if p.Name == "main" && strings.HasSuffix(p.ImportPath, ".test") && p.ForTest == "" {
			pkg := strings.TrimSuffix(p.ImportPath, ".test")
			deps := make([]string, 0, len(p.Deps))
			for _, d := range p.Deps {
				deps = append(deps, stripTestVariant(d))
			}
			g.testDeps[pkg] = deps
			continue
		}
		dir := filepath.Clean(p.Dir)
		if realRoot != absRoot && !within(absRoot, dir) && within(realRoot, dir) {
			if rel, rerr := filepath.Rel(realRoot, dir); rerr == nil {
				dir = filepath.Join(absRoot, rel)
			}
		}
		if !within(absRoot, dir) {
			continue // the module cache, a sibling checkout: not this diff
		}
		path := stripTestVariant(p.ImportPath)
		if p.ForTest == "" {
			g.dirPkg[dir] = path
		}
		for _, files := range [][]string{p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
			for _, f := range files {
				g.embedPkg[filepath.Join(dir, filepath.FromSlash(f))] = path
			}
		}
	}
	// Only the module's own packages are candidates to test.
	for pkg := range g.testDeps {
		found := false
		for _, p := range g.dirPkg {
			if p == pkg {
				found = true
				break
			}
		}
		if !found {
			delete(g.testDeps, pkg)
		}
	}
	return g, nil
}

// owner maps a changed project-relative file to the package it changes.
func (g *packageGraph) owner(rel string) (string, bool) {
	abs := filepath.Join(g.root, filepath.FromSlash(rel))
	if pkg, ok := g.embedPkg[abs]; ok {
		return pkg, true
	}
	dir := filepath.Dir(abs)
	if strings.HasSuffix(rel, ".go") && !underTestdata(rel) {
		if pkg, ok := g.dirPkg[dir]; ok {
			return pkg, true
		}
		return "", false
	}
	// A package's own testdata/, at any depth below it.
	for d := dir; within(g.root, d); d = filepath.Dir(d) {
		if filepath.Base(d) == "testdata" {
			pkg, ok := g.dirPkg[filepath.Dir(d)]
			return pkg, ok
		}
		if d == g.root {
			break
		}
	}
	return "", false
}

// ignoredByGlobs matches rel against affected_ignore globs: a pattern with no
// slash matches the base name anywhere (`*.md`), `dir/**` matches everything
// under dir, anything else matches the whole path (path.Match syntax).
func ignoredByGlobs(rel string, globs []string) bool {
	rel = filepath.ToSlash(rel)
	for _, g := range globs {
		g = strings.TrimPrefix(filepath.ToSlash(g), "./")
		switch {
		case strings.HasSuffix(g, "/**"):
			if strings.HasPrefix(rel, strings.TrimSuffix(g, "**")) {
				return true
			}
		case !strings.Contains(g, "/"):
			if ok, _ := path.Match(g, path.Base(rel)); ok {
				return true
			}
		default:
			if ok, _ := path.Match(g, rel); ok {
				return true
			}
		}
	}
	return false
}

func underTestdata(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "testdata" {
			return true
		}
	}
	return false
}

// stripTestVariant turns "p [q.test]" into "p".
func stripTestVariant(path string) string {
	if i := strings.Index(path, " ["); i >= 0 {
		return path[:i]
	}
	return path
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func uniqueLines(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func runGoList(ctx context.Context, dir string, env []string) (io.Reader, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-test",
		"-json=ImportPath,Name,Dir,ForTest,Standard,Deps,EmbedFiles,TestEmbedFiles,XTestEmbedFiles",
		"./...")
	cmd.Dir = dir
	if env == nil {
		env = goexec.Env()
	}
	// The caller's env is already scrubbed (goexec.Env); a go list that
	// inherited GOFLAGS=-mod=mod would rewrite go.mod while choosing tests.
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return bytes.NewReader(out), nil
}
