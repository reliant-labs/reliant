package cigate

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// Rules add to reader selections, but affected_ignore opts out of the scan.
func TestAffectedReaders_SelectClaimedAndIgnoredFiles(t *testing.T) {
	cases := []struct {
		name   string
		ignore []string
		rules  []config.CIGateAffectedRule
	}{
		{name: "claimed by a rule", rules: []config.CIGateAffectedRule{{Paths: []string{"config/**"}, Packages: []string{"./a"}}}},
		{name: "listed in affected_ignore", ignore: []string{"config/**"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, tracked := readerRoot(t)
			g := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{"config/settings.yaml"}}, tracked: tracked}
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root),
				Readers: true, Ignore: tc.ignore, Rules: tc.rules})
			if a.Full {
				t.Fatalf("full (%s), want narrowed", a.Reason)
			}
			wantReader := len(tc.ignore) == 0
			if slices.Contains(a.Packages, "ex.com/m/c") != wantReader {
				t.Errorf("packages = %v, want reader selected = %v", a.Packages, wantReader)
			}
			if !wantReader && len(a.Selections) != 0 {
				t.Errorf("ignored file produced selections: %v", a.Selections)
			}
		})
	}
}

// Ignore suppresses implicit ownership for source, embeds and testdata,
// without overriding an explicit rule selecting a package.
func TestComputeAffected_IgnoreOwnedFiles(t *testing.T) {
	for _, file := range []string{"a/a.go", "a/tmpl/x.tmpl", "a/testdata/input.json"} {
		t.Run(file, func(t *testing.T) {
			root := t.TempDir()
			for _, explicit := range []bool{false, true} {
				var rules []config.CIGateAffectedRule
				if explicit {
					rules = []config.CIGateAffectedRule{{Paths: []string{file}, Packages: []string{"./c"}}}
				}
				a := ComputeAffected(context.Background(), AffectedInput{Root: root, GoList: listed(root),
					Git: fakeGit{branch: "f", changed: []string{file}}.run, Ignore: []string{file}, Rules: rules})
				if a.Full || len(a.Build) != 0 || slices.Contains(a.Packages, "ex.com/m/a") || slices.Contains(a.Packages, "ex.com/m/b") {
					t.Fatalf("ignored owner selected: %+v", a)
				}
				if slices.Contains(a.Packages, "ex.com/m/c") != explicit {
					t.Errorf("packages = %v, explicit rule = %v", a.Packages, explicit)
				}
			}
		})
	}
}

// An `on: go-source` rule (a tree-wide walker) fires for a Go change inside
// a nested module affected_modules: auto splits off.
func TestComputeAffected_GoSourceRuleSeesNestedModules(t *testing.T) {
	root, list := modulesFixture(t)
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: AffectedModulesAuto, GoList: list,
		Git:   fakeGit{branch: "f", changed: []string{"sdk/go/util/u.go"}}.run,
		Rules: []config.CIGateAffectedRule{{On: config.CIGateOnGoSource, Packages: []string{"./other"}}}})
	if a.Full {
		t.Fatalf("full (%s), want narrowed", a.Reason)
	}
	if !slices.Contains(a.Packages, "ex.com/m/other") {
		t.Errorf("packages = %v, want ex.com/m/other (the go-source walker)", a.Packages)
	}
}

// The root graph has no nested test binaries. Explicit walker selections
// must reach the nested row, including packages unrelated to the change.
func TestComputeAffected_GoSourceRuleSelectsNestedTests(t *testing.T) {
	for _, pattern := range []string{"./sdk/go/util", "./sdk/go/...", "ex.com/sdk/util", "ex.com/sdk/..."} {
		for _, except := range []bool{false, true} {
			t.Run(pattern+"/except="+fmt.Sprint(except), func(t *testing.T) {
				root, list := modulesFixture(t)
				rule := config.CIGateAffectedRule{On: config.CIGateOnGoSource, Packages: []string{pattern}}
				if except {
					rule.Except = []string{"sdk/go/client/**"}
				}
				a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: AffectedModulesAuto, GoList: list,
					Git:   fakeGit{branch: "f", changed: []string{"sdk/go/client/c_test.go"}}.run,
					Rules: []config.CIGateAffectedRule{rule}})
				if a.Full || len(a.Modules) != 1 || a.Modules[0].Full {
					t.Fatalf("want narrowed nested tests: %+v", a)
				}
				if got := slices.Contains(a.Modules[0].Packages, "ex.com/sdk/util"); got == except {
					t.Errorf("nested packages = %v, want util selected = %v", a.Modules[0].Packages, !except)
				}
				if !slices.Contains(a.Modules[0].Packages, "ex.com/sdk/client") {
					t.Errorf("lost changed package's tests: %v", a.Modules[0].Packages)
				}
			})
		}
	}
}

// --plan --json reports the nested modules' tests, always as a list.
func TestDocument_ReportsModules(t *testing.T) {
	d := (&Affected{}).Document()
	if d.Modules == nil {
		t.Fatal("modules is nil, want []")
	}
	d = (&Affected{Modules: []ModuleTests{{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/x"}}}}).Document()
	if len(d.Modules) != 1 || d.Modules[0].Dir != "sdk/go" {
		t.Errorf("modules = %+v", d.Modules)
	}
}
