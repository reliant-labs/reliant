package lint

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// noCompiledGoFixture is a module whose scope paths each hold Go source that
// golangci-lint's default pass cannot see. reliant's merge gate, 2026-10-23:
// `forge lint --scope scripts` on a scripts-only pull request failed
// golangci-lint with "no go files to analyze", because scripts/ held a
// `//go:build ignore` program beside its shell scripts and the scope counted
// the .go file as a package.
func noCompiledGoFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "plan9" {
		t.Skip("the fixture's foreign-GOOS files are plan9 files")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"main.go":             "package main\n\nfunc main() {}\n",
		"internal/foo/foo.go": "package foo\n",
		// Shell plus a `go run`-only program: no pass compiles it.
		"scripts/a.sh":   "#!/bin/sh\necho hi\n",
		"scripts/gen.go": "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		// Only the integration pass compiles it.
		"itest/x_test.go": "//go:build integration\n\npackage itest\n",
		// Another module's source is not this module's package.
		"tools/nested/go.mod": "module example.com/nested\n\ngo 1.22\n",
		"tools/nested/n.go":   "package nested\n",
		// Excluded on this host, by file name and by constraint.
		"foreign/x_plan9.go":    "package foreign\n",
		"foreign2/y.go":         "//go:build plan9\n\npackage foreign2\n",
		"docs/README.md":        "notes\n",
		"scripts/testdata/t.go": "package t\n",
	})
	return root
}

func TestLintScopePackagesCountOnlyCompiledGo(t *testing.T) {
	root := noCompiledGoFixture(t)
	for _, tc := range []struct {
		name        string
		scope       []string
		wantAny     []string // goPackages: some lint pass compiles it
		wantDefault []string // defaultPassPackages: the untagged build compiles it
	}{
		{name: "ignore-tagged program beside shell scripts", scope: []string{"scripts"}},
		{name: "the ignore-tagged file named directly", scope: []string{"scripts/gen.go"}},
		{name: "a nested module", scope: []string{"tools"}},
		{name: "a nested module's file", scope: []string{"tools/nested/n.go"}},
		{name: "foreign GOOS by file name and by constraint", scope: []string{"foreign", "foreign2"}},
		{name: "docs only", scope: []string{"docs"}},
		{
			name:    "integration-tagged tests stay in scope for the tagged pass",
			scope:   []string{"itest"},
			wantAny: []string{"./itest/..."},
		},
		{
			name:    "an integration-tagged file names its package",
			scope:   []string{"itest/x_test.go"},
			wantAny: []string{"./itest"},
		},
		{
			name:        "a root-level file is the root package",
			scope:       []string{"main.go"},
			wantAny:     []string{"."},
			wantDefault: []string{"."},
		},
		{
			// The mixed case: only the dirs holding compiled Go survive.
			name:        "mixed scope keeps only the dirs holding Go",
			scope:       []string{"internal/foo", "scripts", "itest", "tools", "docs", "foreign"},
			wantAny:     []string{"./internal/foo/...", "./itest/..."},
			wantDefault: []string{"./internal/foo/..."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseLintScope(root, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if got := sorted(s.goPackages()); !reflect.DeepEqual(got, sorted(tc.wantAny)) {
				t.Errorf("goPackages = %v, want %v", got, tc.wantAny)
			}
			got, _ := defaultPassPackages(root, s.goPackages(), nil)
			if got := sorted(got); !reflect.DeepEqual(got, sorted(tc.wantDefault)) {
				t.Errorf("defaultPassPackages = %v, want %v", got, tc.wantDefault)
			}
		})
	}
}

// A tag golangci-lint's own run.build-tags lists is part of the default pass,
// so its files count, and a config tag combines with a project tag the way
// the per-tag pass runs them.
func TestLintScopeGoPackagesHonoursGolangciBuildTags(t *testing.T) {
	root := noCompiledGoFixture(t)
	writeTree(t, root, map[string]string{
		".golangci.yml":  "version: \"2\"\nrun:\n  build-tags:\n    - linting\n",
		"both/b_test.go": "//go:build linting && integration\n\npackage both\n",
		"cfgonly/c.go":   "//go:build linting\n\npackage cfgonly\n",
		"cfgnever/n.go":  "//go:build !linting\n\npackage cfgnever\n",
	})
	s, err := parseLintScope(root, []string{"both", "cfgonly", "cfgnever"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sorted(s.goPackages()), []string{"./both/...", "./cfgonly/..."}; !reflect.DeepEqual(got, want) {
		t.Errorf("goPackages = %v, want %v", got, want)
	}
}

// A scope with no compiled Go is vacuous for every package lane: each is
// skipped with a one-line note, never run on nothing (which golangci-lint
// fails) and never run on the whole module (an empty pattern list).
func TestScopeWithNoCompiledGoSkipsPackageLanes(t *testing.T) {
	root := noCompiledGoFixture(t)
	scope, err := parseLintScope(root, []string{"scripts", "docs"})
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	steps := []linterStep{}
	for _, name := range []string{"golangci-lint", "contract linter"} {
		steps = append(steps, linterStep{
			name: name, gates: true, scope: scopeByPackages,
			shouldRun: func(*lintRunCtx) (bool, string) { return true, "" },
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				ran = append(ran, name+" on "+strings.Join(rc.paths, " "))
				return nil, false, nil
			},
		})
	}
	for _, o := range collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}, scope: scope}, steps) {
		if o.ran || o.gated || o.unscoped {
			t.Errorf("%s: ran=%v gated=%v unscoped=%v; want skipped", o.name, o.ran, o.gated, o.unscoped)
		}
		if want := "no Go package under --scope — skipping " + o.name; o.skipMsg != want {
			t.Errorf("%s: skipMsg = %q, want %q", o.name, o.skipMsg, want)
		}
	}
	if len(ran) != 0 {
		t.Errorf("package lanes ran on a scope with no compiled Go: %v", ran)
	}
}

// The targeted `--contract` / `--exported-vars` lanes skip the same way. An
// empty pattern list must never reach the analysis: it would load ./....
func TestTargetedPackageLanesSkipScopeWithNoCompiledGo(t *testing.T) {
	root := noCompiledGoFixture(t)
	for _, scopePaths := range [][]string{{"scripts"}, {"scripts/gen.go"}, {"itest"}, {"itest/x_test.go"}} {
		scope, err := parseLintScope(root, scopePaths)
		if err != nil {
			t.Fatal(err)
		}
		for lane := range targetedPackageLanes {
			o, err := collectLaneOutcome(context.Background(), lintFlags{}, lane, []string{"./..."}, root, nil, nil, scope)
			if err != nil {
				t.Fatalf("%s --scope %v: %v", lane, scopePaths, err)
			}
			if o.ran || o.skipMsg != "no Go package under --scope — skipping "+lane {
				t.Errorf("%s --scope %v: ran=%v skipMsg=%q; want skipped", lane, scopePaths, o.ran, o.skipMsg)
			}
		}
	}
}

// A scope reaching only tagged Go still runs the contract linter's step, but
// hands its untagged analysis nothing, rather than a pattern go/packages
// would report as "build constraints exclude all Go files".
func TestContractStepUnderTagOnlyScopeIsVacuous(t *testing.T) {
	root := noCompiledGoFixture(t)
	scope, err := parseLintScope(root, []string{"itest/x_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range lintPipeline() {
		if step.name != "contract linter" {
			continue
		}
		rc := &lintRunCtx{cwd: root, paths: scope.goPackages(), scope: scope}
		fs, gated, err := step.collect(rc)
		if err != nil || gated || len(fs) != 0 {
			t.Fatalf("contract lane on a tag-only scope: gated=%v err=%v findings=%+v; want vacuous", gated, err, fs)
		}
		return
	}
	t.Fatal(`no "contract linter" step in lintPipeline`)
}

// The golangci-lint lane end to end: the default pass gets only the patterns
// the untagged build compiles, and a scope reaching only tagged Go runs the
// tagged pass alone.
func TestGolangciLaneUnderScopeSkipsPatternsWithoutCompiledGo(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through shell subprocesses; runs in task test")
	}
	root := noCompiledGoFixture(t)
	for _, tc := range []struct {
		name  string
		scope []string
		want  []string // one golangci-lint argv tail per run, in order
	}{
		{
			name:  "mixed",
			scope: []string{"internal/foo", "scripts", "itest", "tools"},
			want:  []string{" ./internal/foo/...", "--build-tags=integration ./itest"},
		},
		{
			name:  "tag-only",
			scope: []string{"itest"},
			want:  []string{"--build-tags=integration ./itest"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv("STUB_LOG", log)
			fakeGolangciLint(t, `echo "$*" >> "$STUB_LOG"; echo "0 issues."`)
			scope, err := parseLintScope(root, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			rc := testRunCtx(t, false)
			rc.cwd, rc.scope, rc.paths = root, scope, scope.goPackages()
			if fs, gated := collectGolangciLintJSON(rc); gated || len(fs) != 0 {
				t.Fatalf("gated=%v findings=%+v", gated, fs)
			}
			raw, _ := os.ReadFile(log)
			calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(calls) != len(tc.want) {
				t.Fatalf("want %d golangci-lint runs, got %d:\n%s", len(tc.want), len(calls), raw)
			}
			for i, want := range tc.want {
				if !strings.HasSuffix(calls[i], want) {
					t.Errorf("run %d = %q, want it to end %q", i, calls[i], want)
				}
				if i == 0 && tc.name == "mixed" && strings.Contains(calls[i], "--build-tags") {
					t.Errorf("the default pass must stay on the default tags: %q", calls[i])
				}
				for _, bad := range []string{"scripts", "tools", " ./..."} {
					if strings.Contains(calls[i], bad) {
						t.Errorf("run %d = %q hands golangci-lint %q, which holds no compiled Go", i, calls[i], bad)
					}
				}
			}
		})
	}
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
