package architecture

import (
	"bytes"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─── the leaf packages stay free of the Temporal SDK ───────────────────────
//
// internal/logging, internal/errclass and internal/db are imported by most of
// the module, so whatever they import is linked into most of its test binaries.
// go.temporal.io/sdk (and the gRPC closure it brings, ~225 packages) used to
// reach them through errclass's Temporal rules and one activity.GetInfo call in
// db, and was linked into ~240 test binaries that never run a workflow.
//
// Temporal-specific code lives beside the SDK instead:
//
//   - internal/errclass/temporalclass registers errclass's Temporal verdicts
//     (Register, from init) and owns temporalclass.Category;
//   - internal/observability/temporalobs holds the worker interceptors.
//
// The invariant has two halves:
//
//   - What a dependent links: the non-test closure of each leaf, for every
//     platform the binaries ship on and every build tag, has no Temporal
//     package. A GOOS-suffixed or tag-constrained file counts as much as any
//     other.
//   - The leaf's own test binary: its test files reach no Temporal package
//     either, except the files in temporalAwareLeafTests, which test how the
//     leaf behaves around Temporal and so must import it.
//
// TestTemporalClassIsLinkedWhereTemporalRuns pins the other side: a package
// that runs or wraps Temporal activities must reach temporalclass, or its
// errors lose their classification silently.

var temporalFreePackages = []string{
	"internal/logging",
	"internal/errclass",
	"internal/db",
}

// temporalAwareLeafTests are the test files of a leaf package allowed to reach
// the Temporal SDK. Only that leaf's own test binary links what they import.
// Every other test file of a leaf is held to the same rule as its source.
var temporalAwareLeafTests = map[string]string{
	"internal/logging/callsite_handler_test.go":     "attributes call sites through Temporal's logger adapter (go.temporal.io/sdk/log)",
	"internal/logging/error_class_test.go":          "logs a benign Temporal ApplicationError as a user error, through temporalclass",
	"internal/errclass/expected_conditions_test.go": "classifies the sentinel errors of launch and triggers, which run workflows",
}

// temporalGOOS are the platforms reliant binaries ship on (the daemon runs on
// all three). The full lane asks the go tool for each.
var temporalGOOS = []string{"linux", "darwin", "windows"}

const (
	temporalGuardReliantModule = "github.com/reliant-labs/reliant"
	temporalModulePath         = "go.temporal.io/"
	temporalClassPkg           = temporalGuardReliantModule + "/internal/errclass/temporalclass"
)

func isModulePackage(importPath string) bool {
	return importPath == temporalGuardReliantModule || strings.HasPrefix(importPath, temporalGuardReliantModule+"/")
}

func packageDir(repo, importPath string) string {
	if importPath == temporalGuardReliantModule {
		return repo
	}
	return filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(importPath, temporalGuardReliantModule+"/")))
}

// packageFileImports returns the imports of every Go file of the in-module package
// importPath, keyed by the file's repo-relative path, ignoring GOOS/GOARCH file
// suffixes and //go:build constraints: a file that builds only on windows, or
// only under a tag, still links what it imports into the binaries built that
// way. Files the go tool never builds (`//go:build ignore`, names starting with
// "_" or ".") are skipped. withTests also reads the _test.go files, both the
// package's own and its external _test package. It parses the files in-process,
// so the test cache sees them.
func packageFileImports(t *testing.T, repo, importPath string, withTests bool) map[string][]string {
	t.Helper()
	dir := packageDir(repo, importPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read in-module package %s at %s: %v", importPath, dir, err)
	}
	out := map[string][]string{}
	hasSource := false
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") && !withTests {
			continue
		}
		file := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatalf("parse imports of %s: %v", file, err)
		}
		if buildIgnored(f.Comments, f.Package) {
			continue
		}
		if !strings.HasSuffix(name, "_test.go") {
			hasSource = true
		}
		rel, err := filepath.Rel(repo, file)
		if err != nil {
			t.Fatal(err)
		}
		imports := make([]string, 0, len(f.Imports))
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import %s: %v", file, spec.Path.Value, err)
			}
			imports = append(imports, imp)
		}
		out[filepath.ToSlash(rel)] = imports
	}
	if !hasSource {
		t.Fatalf("in-module package %s at %s has no non-test Go source", importPath, dir)
	}
	return out
}

// buildIgnored reports whether the file's //go:build line, among the comments
// before its package clause, is the `ignore` convention for files no build
// includes (generators run with go run).
func buildIgnored(comments []*ast.CommentGroup, pkgPos token.Pos) bool {
	for _, group := range comments {
		if group.Pos() >= pkgPos {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false
			}
			if tag, ok := expr.(*constraint.TagExpr); ok && tag.Tag == "ignore" {
				return true
			}
		}
	}
	return false
}

// externalReach is one import that leaves the module, with the in-module chain
// that reaches it: from the file it starts at to the package that imports it.
type externalReach struct {
	chain    []string
	external string
}

// sourceImports returns a reader of the non-test, all-constraints imports of an
// in-module package (packageFileImports without tests, flattened) that parses
// each package once. It is not safe for concurrent use.
func sourceImports(t *testing.T, repo string) func(importPath string) []string {
	t.Helper()
	cache := map[string][]string{}
	return func(importPath string) []string {
		if imps, ok := cache[importPath]; ok {
			return imps
		}
		var imps []string
		for _, fileImps := range packageFileImports(t, repo, importPath, false) {
			imps = append(imps, fileImps...)
		}
		cache[importPath] = imps
		return imps
	}
}

// moduleReach walks the non-test, all-constraints closure (read by read) of the
// in-module imports and returns every import leaving the module, each with the
// shortest in-module chain that reaches it. start is the first link of each
// chain (a file or a package).
func moduleReach(read func(string) []string, start string, imports []string) []externalReach {
	type node struct {
		pkg   string
		chain []string
	}
	var (
		out     []externalReach
		visited = map[string]bool{}
		queue   []node
	)
	enqueue := func(from []string, imp string) {
		if !isModulePackage(imp) {
			out = append(out, externalReach{chain: from, external: imp})
			return
		}
		if visited[imp] {
			return
		}
		visited[imp] = true
		queue = append(queue, node{pkg: imp, chain: append(append([]string(nil), from...), imp)})
	}
	for _, imp := range imports {
		enqueue([]string{start}, imp)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range read(cur.pkg) {
			enqueue(cur.chain, imp)
		}
	}
	return out
}

// temporalReaches returns, sorted, a description of each Temporal import in
// reaches.
func temporalReaches(reaches []externalReach) []string {
	var bad []string
	for _, r := range reaches {
		if strings.HasPrefix(r.external, temporalModulePath) {
			bad = append(bad, strings.Join(r.chain, " → ")+" imports "+r.external)
		}
	}
	sort.Strings(bad)
	return bad
}

// TestLeafPackagesImportNoTemporalSource fails when the closure of a leaf
// package, inside this module, imports the Temporal SDK: from any of its
// source files on any platform or build tag, or from a test file not in
// temporalAwareLeafTests. It reads source in-process, so it runs under -short
// and caches soundly; TestLeafPackagesDoNotLinkTemporal is the full-lane check
// that also sees modules outside this one.
func TestLeafPackagesImportNoTemporalSource(t *testing.T) {
	t.Parallel()
	repo := root(t)
	for _, leaf := range temporalFreePackages {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			leafPkg := temporalGuardReliantModule + "/" + leaf
			read := sourceImports(t, repo)
			files := packageFileImports(t, repo, leafPkg, true)
			var source []string
			for file, imps := range files {
				if !strings.HasSuffix(file, "_test.go") {
					source = append(source, imps...)
				}
			}
			if len(source) == 0 {
				t.Fatalf("read no imports of %s's source; the walk is broken", leaf)
			}
			if bad := temporalReaches(moduleReach(read, leaf, source)); len(bad) > 0 {
				t.Errorf("%s must not reach the Temporal SDK; every test binary that links it would carry ~225 packages. "+
					"Put Temporal-specific code in internal/errclass/temporalclass or internal/observability/temporalobs "+
					"(or inject it at wiring time):\n  %s", leaf, strings.Join(bad, "\n  "))
				return // every test file reaches the source too; fix that first
			}
			for file, imps := range files {
				if !strings.HasSuffix(file, "_test.go") {
					continue
				}
				if _, ok := temporalAwareLeafTests[file]; ok {
					continue
				}
				if bad := temporalReaches(moduleReach(read, file, imps)); len(bad) > 0 {
					t.Errorf("%s links the Temporal SDK into %s's test binary. Test Temporal behaviour beside the SDK "+
						"(internal/errclass/temporalclass, internal/observability/temporalobs), or, if this test is about how %s "+
						"behaves around Temporal, add it to temporalAwareLeafTests with the reason:\n  %s",
						file, leaf, leaf, strings.Join(bad, "\n  "))
				}
			}
		})
	}
}

// TestTemporalAwareLeafTestsExist keeps the exemption list honest: an entry
// whose file is gone, or that names no leaf's test file, is removed.
func TestTemporalAwareLeafTestsExist(t *testing.T) {
	t.Parallel()
	repo := root(t)
	for file := range temporalAwareLeafTests {
		dir := path.Dir(file)
		leaf := false
		for _, l := range temporalFreePackages {
			leaf = leaf || dir == l
		}
		if !leaf || !strings.HasSuffix(file, "_test.go") {
			t.Errorf("temporalAwareLeafTests: %s is not a test file of a package in temporalFreePackages", file)
			continue
		}
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(file))); err != nil {
			t.Errorf("temporalAwareLeafTests: %s: %v; remove the entry", file, err)
		}
	}
}

// TestLeafPackagesDoNotLinkTemporal asks the go tool for the full dependency
// closure, which also covers modules outside this one (forge/pkg/...), once per
// shipped GOOS. A leaf with no file in temporalAwareLeafTests is also checked
// with -test, so its own test binary stays Temporal-free. It execs go, which
// Go's test cache cannot track, so it runs in the full lane only; the source
// walk above is its -short counterpart.
func TestLeafPackagesDoNotLinkTemporal(t *testing.T) {
	if testing.Short() {
		t.Skip("execs `go list -deps` (invisible to the test cache); task test runs it, and TestLeafPackagesImportNoTemporalSource covers -short")
	}
	repo := root(t)
	for _, leaf := range temporalFreePackages {
		withTests := true
		for file := range temporalAwareLeafTests {
			if path.Dir(file) == leaf {
				withTests = false
			}
		}
		for _, goos := range temporalGOOS {
			t.Run(leaf+"/"+goos, func(t *testing.T) {
				args := []string{"list", "-deps", "-f", "{{.ImportPath}}"}
				if withTests {
					args = append(args, "-test")
				}
				args = append(args, "./"+leaf)
				cmd := exec.CommandContext(t.Context(), "go", args...)
				cmd.Dir = repo
				cmd.Env = append(os.Environ(), "GOOS="+goos)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("GOOS=%s go %s: %v\n%s", goos, strings.Join(args, " "), err, stderr.String())
				}
				var temporal []string
				for _, dep := range strings.Fields(string(out)) {
					if strings.HasPrefix(dep, temporalModulePath) {
						temporal = append(temporal, dep)
					}
				}
				if len(temporal) > 0 {
					t.Errorf("GOOS=%s go %s contains %d go.temporal.io package(s), first %s",
						goos, strings.Join(args, " "), len(temporal), temporal[0])
				}
			})
		}
	}
}

// TestTemporalClassIsLinkedWhereTemporalRuns pins the registration: errclass
// reads a Temporal error's verdict only once temporalclass's init has run, so
// every package that executes or wraps Temporal activities must link it, and so
// must each process entry point. It walks all source-file imports, including
// GOOS/GOARCH-specific and tag-constrained files, rather than only the host build.
func TestTemporalClassIsLinkedWhereTemporalRuns(t *testing.T) {
	t.Parallel()
	repo := root(t)
	for _, pkg := range []string{
		"cmd/reliant/cmd",
		"internal/app",
		"internal/workersetup",
		"internal/workflow/runtime",
		"internal/workflow/runtime/activities/handlers",
		"internal/workflow/reconciliation",
	} {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			read := sourceImports(t, repo)
			reached := map[string]bool{}
			queue := []string{path.Join(temporalGuardReliantModule, pkg)}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				if reached[cur] {
					continue
				}
				reached[cur] = true
				for _, imp := range read(cur) {
					if isModulePackage(imp) {
						queue = append(queue, imp)
					}
				}
			}
			if !reached[temporalClassPkg] {
				t.Errorf("%s does not link %s: errclass would not classify Temporal errors "+
					"(a benign ApplicationError is a user error, a CanceledError is canceled) in a process that runs it", pkg, temporalClassPkg)
			}
		})
	}
}
