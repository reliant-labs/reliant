package architecture

import (
	"bytes"
	"errors"
	"go/build"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
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
// TestTemporalClassIsLinkedWhereTemporalRuns pins the other half: a package
// that runs or wraps Temporal activities must reach temporalclass, or its
// errors lose their classification silently.

var temporalFreePackages = []string{
	"internal/logging",
	"internal/errclass",
	"internal/db",
}

const (
	reliantModule      = "github.com/reliant-labs/reliant"
	temporalModulePath = "go.temporal.io/"
	temporalClassPkg   = reliantModule + "/internal/errclass/temporalclass"
)

// moduleImports returns, for the in-module package importPath, every import of
// its non-test files for the host's build constraints. It reads the files
// itself (go/build), so the test cache sees them, unlike a `go list` child.
func moduleImports(t *testing.T, repo, importPath string) []string {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(importPath, reliantModule+"/")))
	pkg, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		var none *build.NoGoError
		if errors.As(err, &none) {
			return nil
		}
		t.Fatalf("read imports of %s: %v", importPath, err)
	}
	return pkg.Imports
}

// moduleClosure walks the in-module imports reachable from roots. It returns
// the in-module packages reached and, for each, the imports that leave the
// module (the first hop outside it: forge, the SDKs, the standard library).
func moduleClosure(t *testing.T, repo string, roots ...string) (reached map[string]bool, external map[string][]string) {
	t.Helper()
	reached = map[string]bool{}
	external = map[string][]string{}
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if reached[cur] {
			continue
		}
		reached[cur] = true
		for _, imp := range moduleImports(t, repo, cur) {
			if imp == reliantModule || strings.HasPrefix(imp, reliantModule+"/") {
				queue = append(queue, imp)
			} else {
				external[cur] = append(external[cur], imp)
			}
		}
	}
	return reached, external
}

// TestLeafPackagesImportNoTemporalSource fails when a package in the closure of
// the leaf packages, inside this module, imports the Temporal SDK. It reads
// source in-process, so it runs under -short and caches soundly.
func TestLeafPackagesImportNoTemporalSource(t *testing.T) {
	t.Parallel()
	repo := root(t)
	for _, leaf := range temporalFreePackages {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			reached, external := moduleClosure(t, repo, reliantModule+"/"+leaf)
			var bad []string
			for pkg, imps := range external {
				for _, imp := range imps {
					if strings.HasPrefix(imp, temporalModulePath) {
						bad = append(bad, pkg+" imports "+imp)
					}
				}
			}
			sort.Strings(bad)
			if len(bad) > 0 {
				t.Errorf("%s must not reach the Temporal SDK; every test binary that links it would carry ~225 packages. "+
					"Put Temporal-specific code in internal/errclass/temporalclass or internal/observability/temporalobs "+
					"(or inject it at wiring time):\n  %s", leaf, strings.Join(bad, "\n  "))
			}
			if len(external[reliantModule+"/"+leaf]) == 0 {
				t.Fatalf("read no imports of %s (walked %d package(s)); the walk is broken", leaf, len(reached))
			}
		})
	}
}

// TestLeafPackagesDoNotLinkTemporal asks the go tool for the full dependency
// closure, which also covers modules outside this one (forge/pkg/...). It
// execs go, which Go's test cache cannot track, so it runs in the full lane
// only; the source walk above is its -short counterpart.
func TestLeafPackagesDoNotLinkTemporal(t *testing.T) {
	if testing.Short() {
		t.Skip("execs `go list -deps` (invisible to the test cache); task test runs it, and TestLeafPackagesImportNoTemporalSource covers -short")
	}
	repo := root(t)
	for _, leaf := range temporalFreePackages {
		t.Run(leaf, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", "./"+leaf)
			cmd.Dir = repo
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps ./%s: %v\n%s", leaf, err, stderr.String())
			}
			var temporal []string
			for _, dep := range strings.Fields(string(out)) {
				if strings.HasPrefix(dep, temporalModulePath) {
					temporal = append(temporal, dep)
				}
			}
			if len(temporal) > 0 {
				t.Errorf("go list -deps ./%s contains %d go.temporal.io package(s), first %s", leaf, len(temporal), temporal[0])
			}
		})
	}
}

// TestTemporalClassIsLinkedWhereTemporalRuns pins the registration: errclass
// reads a Temporal error's verdict only once temporalclass's init has run, so
// every package that executes or wraps Temporal activities must link it, and so
// must each process entry point.
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
			reached, _ := moduleClosure(t, repo, path.Join(reliantModule, pkg))
			if !reached[temporalClassPkg] {
				t.Errorf("%s does not link %s: errclass would not classify Temporal errors "+
					"(a benign ApplicationError is a user error, a CanceledError is canceled) in a process that runs it", pkg, temporalClassPkg)
			}
		})
	}
}
