package cigate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/config"
)

// Nested replaced modules (ci.gate.affected_modules: auto). A root go.mod
// `replace <mod> => ./dir` builds dir's own module into the root, but the
// root's `go list ./...` stops at dir's go.mod, so without this a change
// under dir is owned by no package and runs the full suite. With it:
//
//   - a non-test .go file maps by directory to the actual root graph,
//     so the root packages whose test binaries link it are selected; if the
//     graph cannot place it, the root conservatively runs its full suite;
//   - the nested module's own affected tests run in a test-mod-<dir> row,
//     `go test` in the module's directory, chosen from its own go list;
//   - its go.mod / go.sum runs that module's full suite. The root goes full
//     only when its own go.mod / go.sum changed too, as it already does.
//
// Any other file under dir (not Go, not go.mod/go.sum) still goes through the
// root's rules — embed, ignore, affected_rules, else the full suite — and
// also runs the nested module's full suite. It is opt-in: the default keeps
// every such change on the full suite, exactly as before.

// AffectedModulesAuto is the ci.gate.affected_modules value that turns it on.
const AffectedModulesAuto = "auto"

// ModuleTests is one nested module's affected tests.
type ModuleTests struct {
	// Dir is the module's directory, project-relative; Path its module path.
	Dir  string `json:"dir"`
	Path string `json:"path"`
	// Full runs the module's whole suite (./...); Reason says why.
	Full   bool   `json:"full"`
	Reason string `json:"reason,omitempty"`
	// Packages are the module's packages to test (affected mode).
	Packages []string `json:"packages"`
}

// ModuleTestName is the row of a nested module's tests: test-mod-sdk-go.
func ModuleTestName(dir string) string {
	return "test-mod-" + strings.Trim(nonName.ReplaceAllString(strings.ToLower(filepath.ToSlash(dir)), "-"), "-")
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// localModule is a root go.mod replace onto a directory inside the project.
type localModule struct{ dir, path string }

// replacedModules reads root's go.mod for `replace X => ./dir` whose dir is
// inside root and holds a go.mod, sorted longest dir first so the innermost
// module owns a file.
func replacedModules(root string) []localModule {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil
	}
	var out []localModule
	seen := map[string]bool{}
	for _, r := range f.Replace {
		if !modfile.IsDirectoryPath(r.New.Path) || filepath.IsAbs(r.New.Path) {
			continue
		}
		dir := path.Clean(filepath.ToSlash(r.New.Path))
		if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") || seen[dir] {
			continue
		}
		mod, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
		if err != nil {
			continue
		}
		mp := modfile.ModulePath(mod)
		if mp == "" {
			continue
		}
		seen[dir] = true
		out = append(out, localModule{dir: dir, path: mp})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].dir) > len(out[j].dir) })
	return out
}

// nestedChanges splits changed into what the replaced local modules take and
// what the root still judges. rootFiles must be resolved against the root's
// actual package graph; rest goes on to selectChanges.
type nestedChanges struct {
	rootFiles []string
	// embedDirs are the absolute directories of nested packages that embed
	// a changed non-Go file (go:embed, not test embeds), for the root graph
	// to map to its importers; unresolved says why that could not be told.
	embedDirs  []string
	unresolved string
	rest       []string
	tests      []ModuleTests
}

func splitNestedModules(ctx context.Context, in AffectedInput, list func(context.Context, string, []string) (io.Reader, error), changed []string) nestedChanges {
	nc := nestedChanges{}
	mods := replacedModules(in.Root)
	if len(mods) == 0 {
		nc.rest = changed
		return nc
	}
	byMod, assets := map[int][]string{}, map[int][]string{}
	for _, f := range changed {
		f = filepath.ToSlash(f)
		i := -1
		for j, m := range mods {
			if strings.HasPrefix(f, m.dir+"/") {
				i = j
				break
			}
		}
		if i < 0 {
			nc.rest = append(nc.rest, f)
			continue
		}
		byMod[i] = append(byMod[i], f)
		rel := strings.TrimPrefix(f, mods[i].dir+"/")
		switch {
		case rel == "go.mod" || rel == "go.sum" || strings.HasSuffix(rel, "_test.go") && !underTestdata(rel):
			// The module's own business: its row runs.
		case strings.HasSuffix(rel, ".go") && !underTestdata(rel):
			// A replacement's module directive need not be the import path
			// used by the root. Resolve its directory after listing the root.
			nc.rootFiles = append(nc.rootFiles, f)
		default:
			assets[i] = append(assets[i], f)
		}
	}
	for i, m := range mods {
		files := byMod[i]
		if len(files) == 0 {
			continue
		}
		mt, g := moduleTests(ctx, in, list, m, files)
		nc.tests = append(nc.tests, mt)
		nc.resolveAssets(g, m, assets[i])
	}
	sort.Slice(nc.tests, func(i, j int) bool { return nc.tests[i].Dir < nc.tests[j].Dir })
	return nc
}

// moduleTests decides one nested module's tests from its own package graph.
func moduleTests(ctx context.Context, in AffectedInput, list func(context.Context, string, []string) (io.Reader, error), m localModule, files []string) (ModuleTests, *packageGraph) {
	mt := ModuleTests{Dir: m.dir, Path: m.path}
	var g *packageGraph
	full := func(why string) (ModuleTests, *packageGraph) { mt.Full, mt.Reason = true, why; return mt, g }
	var rels []string
	for _, f := range files {
		rel := strings.TrimPrefix(f, m.dir+"/")
		if rel == "go.mod" || rel == "go.sum" {
			return full(f + " changed")
		}
		rels = append(rels, rel)
	}
	dir := filepath.Join(in.Root, filepath.FromSlash(m.dir))
	r, err := list(ctx, dir, in.Env)
	if err != nil {
		return full("go list failed: " + firstLine(err.Error()))
	}
	if g, err = readPackageGraph(r, dir); err != nil {
		g = nil
		return full("cannot read go list output: " + err.Error())
	}
	// Rules use project-relative paths even when the selected tests run in
	// a nested module. Resolve them with this module's test graph, anchored
	// at the project root, rather than the root graph (which omits them).
	ruleGraph := *g
	ruleGraph.root = in.Root
	rulePkgs := map[string]bool{}
	for _, f := range files {
		for _, rule := range in.Rules {
			if matched, _ := ruleMatch(f, rule); !matched {
				continue
			}
			if rule.Full {
				// Keep existing full-lane handling; this path also evaluates
				// path-only package selections, not new whole-suite triggers.
				if rule.On == config.CIGateOnGoSource {
					return full(f + " changed — " + rule.Reason)
				}
				continue
			}
			for _, pkg := range ruleGraph.rulePackages(f, rule) {
				rulePkgs[pkg] = true
			}
		}
	}
	changed := map[string]bool{}
	for _, rel := range rels {
		pkg, ok := g.owner(rel)
		if !ok {
			return full(path.Join(m.dir, rel) + " is not Go source, an embedded file or testdata of a package in the module")
		}
		changed[pkg] = true
	}
	for pkg, deps := range g.testDeps {
		hit := changed[pkg] || rulePkgs[pkg]
		for _, d := range deps {
			hit = hit || changed[d]
		}
		if hit {
			mt.Packages = append(mt.Packages, pkg)
		}
	}
	sort.Strings(mt.Packages)
	return mt, g
}

// resolveAssets settles a nested module's changed non-Go files by the
// module's own embed ownership. One a nested package go:embeds changes that
// package's build, which root importers link: its directory goes to
// embedDirs. One embedded only by tests or under a package's testdata reaches
// only the nested tests; one nobody owns stays with the root's own rules
// (rest), as before. Without the module's graph (g nil) ownership is unknown, and the
// root runs whole.
func (nc *nestedChanges) resolveAssets(g *packageGraph, m localModule, files []string) {
	if len(files) == 0 {
		return
	}
	if g == nil {
		if nc.unresolved == "" {
			nc.unresolved = files[0] + " changed in nested module " + m.path + ", whose package graph could not be read, so which root packages embed it is unknown"
		}
		return
	}
	pkgDir := map[string]string{}
	for d, p := range g.dirPkg {
		pkgDir[p] = d
	}
	for _, f := range files {
		rel := strings.TrimPrefix(f, m.dir+"/")
		pkg, ok := g.prodEmbedPkg[filepath.Join(g.root, filepath.FromSlash(rel))]
		if !ok {
			if _, nested := g.owner(rel); !nested {
				nc.rest = append(nc.rest, f)
			}
			// A test embed or a package's testdata: only that package's test
			// binary reads it, and no root binary links one; its row runs.
			continue
		}
		d, ok := pkgDir[pkg]
		if !ok {
			if nc.unresolved == "" {
				nc.unresolved = f + " is embedded by " + pkg + ", whose directory the nested module's graph does not list"
			}
			continue
		}
		nc.embedDirs = append(nc.embedDirs, d)
	}
}

// WithModuleTests adds a test-mod-<dir> row after unit-tests for each nested
// module a.Modules names. Without affected_modules (a.Modules empty) checks
// come back unchanged.
func WithModuleTests(checks []Check, a Affected) []Check {
	if len(a.Modules) == 0 {
		return checks
	}
	at := -1
	taken := map[string]bool{}
	for i, c := range checks {
		taken[c.Name] = true
		if c.Name == UnitTests {
			at = i
		}
	}
	if at < 0 || checks[at].Deferred != "" {
		return checks
	}
	var rows []Check
	for _, m := range a.Modules {
		name := ModuleTestName(m.Dir)
		if taken[name] {
			continue // a project row of this name wins
		}
		c := Check{Name: name, Lane: LaneTest, Group: GroupTest, Heavy: true, Requires: []string{"go"}, Dir: m.Dir}
		switch {
		case m.Full:
			c.Argv = []string{"go", "test", "./..."}
			c.Note = "nested module " + m.Path + ": full suite — " + m.Reason
		case len(m.Packages) == 0:
			c.NotApplicable = "affected mode: no package with tests in " + m.Dir + " reaches its changes"
		default:
			c.Argv = append([]string{"go", "test"}, m.Packages...)
			c.Note = fmt.Sprintf("nested module %s: affected mode, %d package(s)", m.Path, len(m.Packages))
		}
		rows = append(rows, c)
	}
	out := make([]Check, 0, len(checks)+len(rows))
	out = append(out, checks[:at+1]...)
	out = append(out, rows...)
	return append(out, checks[at+1:]...)
}

// annotateModuleFiles says, in each changed file's report, which nested
// module's test-mod row it runs. A file only the module judges (its tests,
// go.mod, go.sum) is not "unclaimed": the root never saw it.
func annotateModuleFiles(files []FileReport, nc nestedChanges, mods []ModuleTests) []FileReport {
	rest := map[string]bool{}
	for _, f := range nc.rest {
		rest[f] = true
	}
	for i := range files {
		f := &files[i]
		for _, m := range mods {
			if !strings.HasPrefix(filepath.ToSlash(f.Path), m.Dir+"/") {
				continue
			}
			note := "nested module " + m.Path + " (" + ModuleTestName(m.Dir) + ")"
			if f.Package == "" && len(f.Rules) == 0 && !f.Ignored && !rest[f.Path] {
				f.Note = note
			} else {
				f.Note += "; " + note
			}
			break
		}
	}
	return files
}
