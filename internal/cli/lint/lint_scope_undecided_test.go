package lint

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestBrokenDependenciesRequiresLoadEvidence(t *testing.T) {
	const dep = "example.com/m/dep"
	for _, tt := range []struct {
		name    string
		visited bool
		failed  bool
		want    []string
	}{
		{name: "unresolved import"},
		{name: "loaded dependency without errors", visited: true},
		{name: "broken dependency", visited: true, failed: true, want: []string{dep}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := &packages.Package{
				ID: "example.com/m/a", PkgPath: "example.com/m/a",
				Errors: []packages.Error{{Msg: "could not import " + dep + " (unavailable)"}},
			}
			if tt.visited {
				p := &packages.Package{ID: dep, PkgPath: dep}
				if tt.failed {
					p.Errors = []packages.Error{{Msg: "undefined: missing"}}
				}
				root.Imports = map[string]*packages.Package{dep: p}
			}
			if got := brokenDependencies([]*packages.Package{root}); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("brokenDependencies = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCouldNotImport(t *testing.T) {
	for msg, want := range map[string][2]string{
		"could not import example.com/m/internal/b (internal/b/b.go:3:2: could not import example.com/m/internal/c (-: # example.com/m/internal/c": {"example.com/m/internal/b", "example.com/m/internal/c"},
		"could not import example.com/m/internal/c (-: # example.com/m/internal/c":                                                                 {"example.com/m/internal/c", "example.com/m/internal/c"},
		"undefined: store.Foo":                            {"", ""},
		"x declared and not used; could not import y (z)": {"", ""},
	} {
		first, root := couldNotImport(msg)
		if first != want[0] || root != want[1] {
			t.Errorf("couldNotImport(%q) = %q, %q; want %q, %q", msg, first, root, want[0], want[1])
		}
	}
}

// undecidedFixture is a module whose internal/a is the scope; b and c lie
// outside it.
func undecidedFixture(t *testing.T) (string, *lintScope) {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.24\n",
		"internal/a/a.go":     "package a\n",
		"internal/a/sub/s.go": "package sub\n",
		"internal/b/b.go":     "package b\n",
	})
	s, err := parseLintScope(dir, []string{"internal/a"})
	if err != nil {
		t.Fatal(err)
	}
	return dir, s
}

// A scoped golangci-lint run whose only errors in the scope are imports of
// broken packages outside it could not judge the scope; any other error in
// the scope is still a failure.
func TestGolangciUndecided(t *testing.T) {
	dir, scope := undecidedFixture(t)
	importB := lintJSONFinding{File: "internal/a/a.go", Line: 3, Severity: lintSevError, Rule: "golangci-lint",
		Message: "could not import example.com/m/internal/b (internal/b/b.go:3:2: could not import example.com/m/internal/c (-: # example.com/m/internal/c"}
	importSub := lintJSONFinding{File: "internal/a/a.go", Line: 4, Severity: lintSevError, Rule: "golangci-lint",
		Message: "could not import example.com/m/internal/a/sub (-: # example.com/m/internal/a/sub"}
	ownErr := lintJSONFinding{File: "internal/a/a.go", Line: 9, Severity: lintSevError, Rule: "golangci-lint", Message: "undefined: b.Foo"}
	outOfScope := lintJSONFinding{File: "internal/b/b.go", Line: 3, Severity: lintSevError, Rule: "golangci-lint", Message: "cannot use x"}
	rawLine := lintJSONFinding{Severity: lintSevError, Rule: "external", Message: "-: # example.com/m/internal/c", under: "internal/a/a.go"}

	scoped := &lintRunCtx{cwd: dir, scope: scope}
	ud := golangciUndecided(scoped, []lintJSONFinding{importB, outOfScope, rawLine})
	if ud == nil {
		t.Fatal("a scope whose only error is an import of a broken out-of-scope package was not undecided")
	}
	if want := []string{"example.com/m/internal/c"}; !reflect.DeepEqual(ud.broken, want) {
		t.Errorf("broken = %v, want the package that started the chain %v", ud.broken, want)
	}
	if !strings.Contains(ud.reason(), "example.com/m/internal/c") {
		t.Errorf("reason %q does not name the broken package", ud.reason())
	}
	for name, fs := range map[string][]lintJSONFinding{
		"an in-scope import is the scope's own breakage": {importSub},
		"another in-scope error still fails":             {importB, ownErr},
		"nothing in the scope failed":                    {outOfScope},
	} {
		if ud := golangciUndecided(scoped, fs); ud != nil {
			t.Errorf("%s: got undecided %+v", name, ud)
		}
	}
	if ud := golangciUndecided(&lintRunCtx{cwd: dir}, []lintJSONFinding{importB}); ud != nil {
		t.Errorf("an unscoped run was undecided: %+v", ud)
	}
}

// Through the engine and the verdict: under --scope an undecided lane is
// named with its reason, never counted as passed, and does not fail the run;
// unscoped the same report fails the lane, as before.
func TestUndecidedLaneUnderScope(t *testing.T) {
	root := scopeFixture(t)
	scope, err := parseLintScope(root, []string{"internal/handlers/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	always := func(*lintRunCtx) (bool, string) { return true, "" }
	undecided := &laneUndecidedError{broken: []string{"example.com/m/internal/store"}, cause: "packages failed to load: store is broken"}
	steps := []linterStep{
		{name: "contract linter", gates: true, scope: scopeByPackages, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return nil, false, undecided }},
		{name: "forge convention lint", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return nil, false, nil }},
	}

	outcomes := collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}, scope: scope}, steps)
	if o := outcomes[0]; o.undecided == "" || o.ran || o.gated {
		t.Fatalf("scoped undecided lane: %+v; want undecided, not ran, not gated", o)
	}
	var buf bytes.Buffer
	if err := renderLintOutcomes(&buf, outcomes, true, scope); err != nil {
		t.Fatalf("an undecided lane failed the scoped run: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"○ contract linter — not decided: the scope imports example.com/m/internal/store",
		"⚠️  1 gating linter(s) passed; 1 NOT DECIDED (contract linter: imports outside the scope do not compile)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	flat, _ := flattenLintOutcomes(outcomes)
	if len(flat) == 0 || !strings.Contains(flat[0].Message, "not decided under --scope") {
		t.Errorf("--json does not name the undecided lane: %+v", flat)
	}

	outcomes = collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}}, steps)
	if o := outcomes[0]; !o.ran || !o.gated || o.undecided != "" || len(o.findings) != 1 ||
		!strings.Contains(o.findings[0].Message, "store is broken") {
		t.Errorf("unscoped, the lane must fail with its cause: %+v", o)
	}
}

// The contract lane's load names a requested package whose only load errors
// come from an import outside the request as undecided, and a requested
// package that does not compile itself as the failure it is.
func TestAnalysisLoadUndecidedOnBrokenDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages; runs in task test")
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.24\n",
		"internal/a/a.go": "package a\n\nimport \"example.com/m/internal/b\"\n\nvar X = b.Y\n",
		"internal/b/b.go": "package b\n\nvar Y int = \"half-written\"\n",
	})
	t.Chdir(dir)
	load := analysisLoad{tests: true, undecidedOnBrokenDeps: true}

	_, err := loadPackagesForAnalysis(context.Background(), []string{"./internal/a"}, load)
	ud, ok := asUndecided(err)
	if !ok {
		t.Fatalf("err = %v, want a laneUndecidedError", err)
	}
	if want := []string{"example.com/m/internal/b"}; !reflect.DeepEqual(ud.broken, want) {
		t.Errorf("broken = %v, want %v", ud.broken, want)
	}
	if !strings.Contains(ud.Error(), "packages failed to load") {
		t.Errorf("the unscoped cause lost the load errors: %q", ud.Error())
	}

	writeFiles(t, dir, map[string]string{"internal/a/own.go": "package a\n\nvar Z int = \"mine\"\n"})
	if _, err := loadPackagesForAnalysis(context.Background(), []string{"./internal/a"}, load); err == nil {
		t.Fatal("a requested package that does not compile loaded cleanly")
	} else if _, ok := asUndecided(err); ok {
		t.Errorf("a requested package's own compile error was reported undecided: %v", err)
	}

	writeFiles(t, dir, map[string]string{"internal/a/own.go": "package a\n"})
	if _, err := loadPackagesForAnalysis(context.Background(), []string{"./internal/a"}, analysisLoad{tests: true}); err == nil {
		t.Fatal("the plain load passed over a broken import")
	} else if errors.As(err, new(*laneUndecidedError)) {
		t.Errorf("a load that did not ask for it reported undecided: %v", err)
	}
}
