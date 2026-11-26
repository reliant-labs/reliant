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
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
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
// proto, the Taskfile, scripts, a README a test happens to read — unless
// forge.yaml's ci.gate.affected_ignore names it, or a ci.gate.affected_rules
// entry claims it. So does the default branch itself, a branch with no
// changes, a missing origin/<default>, and an explicit request (CI_FULL=1).
// The mode and its reason are always printed. (A migration under
// db/migrations/ is embedded by the generated db/embed_gen.go, so it maps to
// the db package like any embedded file.)
//
// An affected rule (config.CIGateAffectedRule) is the project saying what a
// path the package graph cannot place selects: Go test packages to run, the
// frontends whose tests run (a test-<name> row, WithFrontendTests), or the
// full suite with a reason. A file a non-full rule matches is claimed: it
// adds what the rule selects and no longer forces the full suite. Rules may
// add packages for Go files too; a Go file keeps its own mapping besides.

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
	// Changed are the changed files, project-relative. Set whenever the
	// branch was diffed — also when an unclaimed file sent the unit lane to
	// the full suite — and empty in full mode (default branch, CI_FULL, a
	// full tier, no base), which is what GateByPaths keys on.
	Changed []string
	// Frontends are the frontends ci.gate.affected_rules selected, by name,
	// sorted (affected mode only).
	Frontends []string
	// Selections say, one line per affected rule that matched, what it
	// claimed and selected.
	Selections []string
	// Reach is the ci.gate.affected_reach the run applied: "all" (also
	// when unset) or "direct", with its TransitiveCap.
	Reach         string
	TransitiveCap int
	// Deferred are the test packages a direct reach left to the full gate:
	// transitive importers past the cap. Sorted; never dropped silently.
	Deferred []string
	// ClosureHits are, per check with GoClosure, the changed files in its
	// Go build closure; ClosureErrs why one could not be computed
	// (ResolveGoClosures).
	ClosureHits map[string][]string
	ClosureErrs map[string]string
	// LockfileChecks are the lockfile-<frontend> rows the diff added
	// (WithLockfileChecks), sorted.
	LockfileChecks []string
	// FullChecks are the gate rows a matching rule's full_checks force to
	// run whole, sorted. Set whenever Changed is.
	FullChecks []string
	// Build are the module packages, with or without tests, whose build
	// contains a changed package: a compile phase's set (affected mode).
	Build []string
	// Why says, per package in Packages and in its order, the first changed
	// file that selected it and the edge it came by (affected_why.go).
	Why []PackageWhy
	// Files reports each changed file's package and claiming rules; set
	// whenever the package graph was read.
	Files []FileReport
	// Modules are the nested replaced modules' own tests
	// (ci.gate.affected_modules: auto; WithModuleTests).
	Modules []ModuleTests
}

// Line is the one line the gate prints about the unit lane's mode.
func (a Affected) Line() string {
	if a.Full {
		return "unit tests: full suite — " + a.Reason
	}
	fe := ""
	if len(a.Frontends) > 0 {
		fe = "; frontend tests: " + strings.Join(a.Frontends, ", ")
	}
	if len(a.Packages) == 0 {
		return fmt.Sprintf("unit tests: affected mode — no package with tests reaches the %d changed file(s) since %s (%s)%s; %s=1 runs the full suite",
			len(a.Changed), a.BaseRef, a.Base, fe, FullEnv) + a.deferredLine()
	}
	return fmt.Sprintf("unit tests: affected mode — %d of %d test packages reach the %d changed file(s) since %s (%s)%s; %s=1 runs the full suite",
		len(a.Packages), a.Total, len(a.Changed), a.BaseRef, a.Base, fe, FullEnv) + a.deferredLine()
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
	// Rules are ci.gate.affected_rules: what a changed file the package
	// graph cannot place selects.
	Rules []config.CIGateAffectedRule
	// Readers turns on the reader scan (ci.gate.affected_readers): a changed
	// file nothing else places selects the test packages whose sources name
	// it (affected_readers.go).
	Readers bool
	// Modules is ci.gate.affected_modules: "auto" maps changes in nested
	// modules the root go.mod replaces onto ./dir (affected_modules.go).
	Modules string
	// Reach is ci.gate.affected_reach; nil is full transitive reach.
	Reach *config.CIGateAffectedReach
	// ChangesOnly stops once the changed files are known, without go list:
	// for a caller that gates checks by path (GateByPaths) but does not
	// narrow the unit lane.
	ChangesOnly bool
	// Files, when non-empty, are the changed files to judge instead of the
	// diff against the base: a what-if (`forge ci gate --plan --files`).
	Files []string
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
	if len(in.Files) > 0 {
		return computeAffectedFiles(ctx, in)
	}
	git := in.Git
	if git == nil {
		git = runGit
	}
	baseRef, defaultBranch := in.BaseRef, ""
	if baseRef == "" {
		baseRef = defaultBaseRef(ctx, git, in.Root)
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
	fullChecks := ruleFullChecks(changed, in.Rules)
	a := Affected{BaseRef: baseRef, Base: short, Changed: changed, FullChecks: fullChecks}
	if in.ChangesOnly {
		return a
	}
	return affectedFromGraph(ctx, in, a)
}

// affectedFromGraph narrows a (its changed files known) by the package graph.
func affectedFromGraph(ctx context.Context, in AffectedInput, a Affected) Affected {
	baseRef, short, changed := a.BaseRef, a.Base, a.Changed
	// After the diff, the unit lane may still run whole; the changed files
	// stay recorded, so path-gated checks are still judged by them.
	var modules []ModuleTests
	fullAfterDiff := func(reason string) Affected {
		return Affected{Full: true, Reason: reason, BaseRef: baseRef, Base: short, Changed: changed, FullChecks: a.FullChecks, Modules: modules}
	}

	list := in.GoList
	if list == nil {
		list = runGoList
	}
	rootChanged := changed
	nested := nestedChanges{}
	if in.Modules == AffectedModulesAuto {
		nested = splitNestedModules(ctx, in, list, changed)
		rootChanged, modules = nested.rest, nested.tests
		a.Modules = modules
	}
	r, err := list(ctx, in.Root, in.Env)
	if err != nil {
		return fullAfterDiff("go list failed: " + firstLine(err.Error()))
	}
	g, err := readPackageGraph(r, in.Root)
	if err != nil {
		return fullAfterDiff("cannot read go list output: " + err.Error())
	}
	if in.Readers {
		git := in.Git
		if git == nil {
			git = runGit
		}
		// No tracked list means no readers: every unplaced file still runs
		// the full suite.
		if tracked, terr := trackedFiles(ctx, git, in.Root); terr == nil {
			g.readers = scanReaders(g, tracked)
		}
	}
	sel, reason := selectChanges(rootChanged, g, in.Ignore, in.Rules)
	files := explainFiles(changed, g, in.Ignore, ruleMatchers(in.Rules))
	files = annotateModuleFiles(files, nested, modules)
	if reason != "" {
		full := fullAfterDiff(reason)
		full.Files = files
		return full
	}
	if nested.unresolved != "" {
		return fullAfterDiff(nested.unresolved)
	}
	for _, d := range nested.embedDirs {
		// A nested package embedding a changed file: the root's own graph
		// names it when any root test binary links it; unlisted, none does.
		if pkg, ok := g.dirPkg[d]; ok {
			sel.changedPkgs[pkg] = true
		}
	}
	// A nested module's Go file is Go source too: rules `on: go-source`
	// (tree-wide walkers) select their packages for it.
	inRoot := map[string]bool{}
	for _, f := range rootChanged {
		inRoot[f] = true
	}
	for _, f := range changed {
		if inRoot[f] || !strings.HasSuffix(f, ".go") {
			continue
		}
		for i, rule := range in.Rules {
			if rule.On != config.CIGateOnGoSource {
				continue
			}
			if matched, _ := ruleMatch(f, rule); !matched {
				continue
			}
			if rule.Full {
				return fullAfterDiff(fmt.Sprintf("%s changed — %s (ci.gate.affected_rules[%d])", f, rule.Reason, i))
			}
			sel.hits[i]++
			for _, pkg := range g.rulePackages(f, rule) {
				sel.rulePkgs[pkg] = true
			}
		}
	}
	for _, f := range nested.rootFiles {
		pkg, ok := g.owner(f)
		if !ok {
			return fullAfterDiff(f + " cannot be mapped to a package in the root graph")
		}
		sel.changedPkgs[pkg] = true
	}
	a.Files = files
	a.Total = len(g.testDeps)
	for pkg, deps := range g.testDeps {
		if sel.changedPkgs[pkg] || sel.rulePkgs[pkg] {
			a.Packages = append(a.Packages, pkg)
			continue
		}
		for _, d := range deps {
			if sel.changedPkgs[d] {
				a.Packages = append(a.Packages, pkg)
				break
			}
		}
	}
	applyReach(&a, g, sel, in.Reach)
	sort.Strings(a.Packages)
	for fe := range sel.frontends {
		a.Frontends = append(a.Frontends, fe)
	}
	sort.Strings(a.Frontends)
	for i, rule := range in.Rules {
		if sel.hits[i] > 0 {
			a.Selections = append(a.Selections, selectionLine(i, rule, sel.hits[i], g))
		}
	}
	a.Selections = append(a.Selections, sel.readerLines...)
	a.Build = g.buildSet(sel.changedPkgs)
	a.Why = explainPackages(a.Packages, files, g, ruleMatchers(in.Rules))
	return a
}

// selection is what the changed files select before the test binaries are
// consulted.
type selection struct {
	changedPkgs map[string]bool // packages the changed files are part of
	rulePkgs    map[string]bool // test packages an affected rule names
	frontends   map[string]bool // frontends an affected rule names
	hits        []int           // per rule, how many changed files it claimed
	readerLines []string        // per file the reader scan placed, what it selected
}

// selectChanges maps each changed file to what it selects, or returns why the
// unit lane must run whole: a full rule matched, or a file nothing claims.
func selectChanges(changed []string, g *packageGraph, ignore []string, rules []config.CIGateAffectedRule) (selection, string) {
	sel := selection{changedPkgs: map[string]bool{}, rulePkgs: map[string]bool{}, frontends: map[string]bool{}, hits: make([]int, len(rules))}
	for _, f := range changed {
		claimed := false
		for i, rule := range rules {
			matched, claims := ruleMatch(f, rule)
			if !matched {
				continue
			}
			if rule.Full {
				why := rule.Reason
				if why == "" {
					why = "the rule runs the full suite"
				}
				return sel, fmt.Sprintf("%s changed — %s (ci.gate.affected_rules[%d])", f, why, i)
			}
			sel.hits[i]++
			claimed = claimed || claims
			for _, pkg := range g.rulePackages(f, rule) {
				sel.rulePkgs[pkg] = true
			}
			for _, fe := range rule.Frontends {
				sel.frontends[fe] = true
			}
		}
		// Explicit rules still apply, but ignored files do not select their
		// owners or readers and cannot force the unowned-file fallback.
		if matchGlobs(f, ignore) {
			continue
		}
		pkg, ok := g.owner(f)
		read := false
		if !strings.HasSuffix(f, ".go") {
			if readers := g.readers.readersOf(f); len(readers) > 0 {
				for p := range readers {
					sel.rulePkgs[p] = true
				}
				sel.readerLines = append(sel.readerLines, readerLine(f, readers))
				read = true
			}
		}
		switch {
		case ok:
			sel.changedPkgs[pkg] = true
		case claimed, read:
		default:
			return sel, fmt.Sprintf("%s changed, and it is not Go source, an embedded file or testdata of a package — nothing proves which tests read it (ci.gate.affected_ignore lists files none do; ci.gate.affected_rules says what a path selects)", f)
		}
	}
	return sel, ""
}

// selectionLine says what affected rule i claimed and selected.
func selectionLine(i int, rule config.CIGateAffectedRule, hits int, g *packageGraph) string {
	var what []string
	for _, pat := range rule.Packages {
		if strings.Contains(pat, DirPlaceholder) {
			what = append(what, pat+" (per matched file)")
			continue
		}
		switch n := len(g.resolve(pat)); n {
		case 0:
			what = append(what, pat+" (no package with tests)")
		case 1:
			what = append(what, pat+" (1 package)")
		default:
			what = append(what, fmt.Sprintf("%s (%d packages)", pat, n))
		}
	}
	if len(rule.PackagesImporting) > 0 {
		what = append(what, fmt.Sprintf("importers of %s (%d packages)", strings.Join(rule.PackagesImporting, ", "), len(g.importers(rule.PackagesImporting))))
	}
	if len(rule.Frontends) > 0 {
		what = append(what, "frontend tests: "+strings.Join(rule.Frontends, ", "))
	}
	if len(rule.FullChecks) > 0 {
		what = append(what, "whole: "+strings.Join(rule.FullChecks, ", "))
	}
	paths := rule.Paths
	if rule.On != "" {
		paths = append(append([]string(nil), paths...), "on: "+rule.On)
	}
	line := fmt.Sprintf("ci.gate.affected_rules[%d] (%s): %d changed file(s) → %s",
		i, strings.Join(paths, ", "), hits, strings.Join(what, "; "))
	if rule.Reason != "" {
		line += " — " + rule.Reason
	}
	return line
}

// GateByPaths settles each path-gated check (Check.Paths, an extra check's
// `paths`): on a branch in affected mode it runs only when a changed file
// matches, and is otherwise N/A, which fails nothing and is never cached. In
// full mode (a.Changed empty: the default branch, CI_FULL, a full tier, no
// base) every check runs.
func GateByPaths(checks []Check, a Affected) []Check {
	out := append([]Check(nil), checks...)
	if len(a.Changed) == 0 {
		return out
	}
	for i := range out {
		c := &out[i]
		if len(c.Paths) == 0 && len(c.GoClosure) == 0 || c.NotApplicable != "" || c.Deferred != "" {
			continue
		}
		if why, ok := a.ClosureErrs[c.Name]; ok {
			c.Note = joinNote(c.Note, "affected mode: Go closure of "+strings.Join(c.GoClosure, ", ")+" unknown ("+why+") — runs")
			continue
		}
		var hit []string
		inClosure := map[string]bool{}
		for _, f := range a.ClosureHits[c.Name] {
			inClosure[f] = true
		}
		for _, f := range a.Changed {
			if len(c.Paths) > 0 && matchGlobs(f, c.Paths) || inClosure[f] {
				hit = append(hit, f)
			}
		}
		if len(hit) == 0 && slices.Contains(a.FullChecks, c.Name) {
			c.Note = "affected mode: run whole by ci.gate.affected_rules full_checks"
			continue
		}
		if len(hit) == 0 {
			var under []string
			if len(c.Paths) > 0 {
				under = append(under, strings.Join(c.Paths, ", "))
			}
			if len(c.GoClosure) > 0 {
				under = append(under, "the Go build closure of "+strings.Join(c.GoClosure, ", "))
			}
			c.NotApplicable = fmt.Sprintf("affected mode: no change under %s since %s (%s=1 runs it)", strings.Join(under, " or "), a.BaseRef, FullEnv)
			continue
		}
		note := "affected mode: " + hit[0] + " changed"
		if len(hit) > 1 {
			note = fmt.Sprintf("affected mode: %s and %d more changed", hit[0], len(hit)-1)
		}
		c.Note = joinNote(c.Note, note)
	}
	return out
}

func joinNote(old, note string) string {
	if old != "" {
		return old + "; " + note
	}
	return note
}

// ApplyAffected narrows the unit-tests row to a's packages. In full mode it
// only annotates the row. Path-gated checks are settled too (GateByPaths).
func ApplyAffected(checks []Check, a Affected) []Check {
	out := GateByPaths(checks, a)
	for i := range out {
		c := &out[i]
		if c.Name != UnitTests || c.NotApplicable != "" {
			continue
		}
		switch {
		case a.Full:
			c.Note = "full suite: " + a.Reason
		case len(a.Packages) == 0 && len(a.Deferred) == 0:
			c.NotApplicable = fmt.Sprintf("affected mode: no package with tests reaches the changes since %s (%s=1 runs the full suite)", a.BaseRef, FullEnv)
		case len(a.Packages) == 0:
			c.NotApplicable = fmt.Sprintf("affected mode: only transitive importers reach the changes since %s; %d deferred to the full gate", a.BaseRef, len(a.Deferred))
		default:
			// `task test -- <packages>` replaces the default ./... with the
			// list. It also skips the frontend lane, which is right: affected
			// mode means every changed file is Go, ignored, or claimed by an
			// affected rule, whose frontends get rows of their own
			// (WithFrontendTests).
			c.Argv = append(append(append([]string(nil), c.Argv...), "--"), a.Packages...)
			c.Note = fmt.Sprintf("affected mode: %d of %d packages (since %s)", len(a.Packages), a.Total, a.BaseRef)
			if len(a.Deferred) > 0 {
				c.Note += fmt.Sprintf("; %d deferred to the full gate", len(a.Deferred))
			}
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
	// prodEmbedPkg is embedPkg for non-test embeds only: the files a
	// package's importers link (affected_modules.go).
	prodEmbedPkg map[string]string
	// directImports maps each module package to what it and its tests
	// (internal and external test files) import directly.
	directImports map[string][]string
	// testDeps maps each module package that has tests to every package its
	// test binary links.
	testDeps map[string][]string
	// readers is the reader scan's index; nil unless it is on.
	readers readerIndex
	// testImports maps a module package to what its test files import.
	testImports map[string]map[string]bool
	// buildDeps maps each module package to every package its build (not
	// its tests) imports, transitively.
	buildDeps map[string][]string
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
	Imports         []string `json:"Imports"`
	TestImports     []string `json:"TestImports"`
	XTestImports    []string `json:"XTestImports"`
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
	g := &packageGraph{root: absRoot, dirPkg: map[string]string{}, embedPkg: map[string]string{}, prodEmbedPkg: map[string]string{}, testDeps: map[string][]string{}, testImports: map[string]map[string]bool{}, buildDeps: map[string][]string{}, directImports: map[string][]string{}}
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
			for _, imp := range append(append([]string(nil), p.TestImports...), p.XTestImports...) {
				if g.testImports[path] == nil {
					g.testImports[path] = map[string]bool{}
				}
				g.testImports[path][imp] = true
			}
			g.buildDeps[path] = p.Deps
		}
		g.addDirectImports(path, p)
		for _, files := range [][]string{p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
			for _, f := range files {
				g.embedPkg[filepath.Join(dir, filepath.FromSlash(f))] = path
			}
		}
		for _, f := range p.EmbedFiles {
			g.prodEmbedPkg[filepath.Join(dir, filepath.FromSlash(f))] = path
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

// resolve returns the module's test packages a ci.gate.affected_rules
// package pattern names: `./dir` (one directory), `./dir/...` (the subtree),
// or an import path in the same two forms.
func (g *packageGraph) resolve(pattern string) []string {
	p := filepath.ToSlash(strings.TrimSpace(pattern))
	sub := p == "..." || strings.HasSuffix(p, "/...")
	base := strings.TrimSuffix(strings.TrimSuffix(p, "..."), "/")
	var out []string
	if base == "." || base == "" && sub || strings.HasPrefix(p, "./") {
		dir := filepath.Join(g.root, filepath.FromSlash(strings.TrimPrefix(base, "./")))
		for d, pkg := range g.dirPkg {
			if _, ok := g.testDeps[pkg]; ok && (d == dir || sub && within(dir, d)) {
				out = append(out, pkg)
			}
		}
	} else {
		for pkg := range g.testDeps {
			if pkg == base || sub && strings.HasPrefix(pkg, base+"/") {
				out = append(out, pkg)
			}
		}
	}
	sort.Strings(out)
	return out
}

// matchGlobs matches rel against affected_ignore-style globs: a pattern with
// no slash matches the base name anywhere (`*.md`), `dir/**` matches
// everything under dir, anything else matches the whole path (path.Match
// syntax).
func matchGlobs(rel string, globs []string) bool {
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

// defaultBaseRef is origin/<default branch> — origin/HEAD's target, else
// origin/main or origin/master — or "" when the clone has none.
func defaultBaseRef(ctx context.Context, git func(ctx context.Context, dir string, args ...string) (string, error), root string) string {
	if ref, err := git(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return strings.TrimPrefix(ref, "refs/remotes/")
	}
	for _, cand := range []string{"origin/main", "origin/master"} {
		if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/remotes/"+cand); err == nil {
			return cand
		}
	}
	return ""
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func runGoList(ctx context.Context, dir string, env []string) (io.Reader, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-test",
		"-json=ImportPath,Name,Dir,ForTest,Standard,Deps,EmbedFiles,TestEmbedFiles,XTestEmbedFiles,Imports,TestImports,XTestImports",
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
