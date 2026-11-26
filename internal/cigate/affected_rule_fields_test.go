package cigate

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// fieldsListed is a module with a guard package (guard), a test helper
// (testutil, no tests) whose importers are b's tests, and generated code
// under gen/x/v1.
func fieldsListed(root string) func(context.Context, string, []string) (io.Reader, error) {
	pkgs := []listedPackage{
		{ImportPath: "ex.com/m/a", Name: "a", Dir: filepath.Join(root, "a")},
		{ImportPath: "ex.com/m/b", Name: "b", Dir: filepath.Join(root, "b"), XTestImports: []string{"ex.com/m/testutil"}},
		{ImportPath: "ex.com/m/c", Name: "c", Dir: filepath.Join(root, "c"), TestImports: []string{"testing"}},
		{ImportPath: "ex.com/m/guard", Name: "guard", Dir: filepath.Join(root, "guard")},
		{ImportPath: "ex.com/m/testutil", Name: "testutil", Dir: filepath.Join(root, "testutil")},
		{ImportPath: "ex.com/m/gen/x/v1", Name: "v1", Dir: filepath.Join(root, "gen", "x", "v1")},
		{ImportPath: "ex.com/m/gen/y/v1", Name: "v1", Dir: filepath.Join(root, "gen", "y", "v1")},
		{ImportPath: "ex.com/m/a.test", Name: "main", Dir: filepath.Join(root, "a"), Deps: []string{"ex.com/m/a"}},
		{ImportPath: "ex.com/m/b.test", Name: "main", Dir: filepath.Join(root, "b"), Deps: []string{"ex.com/m/b", "ex.com/m/testutil"}},
		{ImportPath: "ex.com/m/c.test", Name: "main", Dir: filepath.Join(root, "c"), Deps: []string{"ex.com/m/c"}},
		{ImportPath: "ex.com/m/guard.test", Name: "main", Dir: filepath.Join(root, "guard"), Deps: []string{"ex.com/m/guard"}},
		{ImportPath: "ex.com/m/gen/x/v1.test", Name: "main", Dir: filepath.Join(root, "gen", "x", "v1"), Deps: []string{"ex.com/m/gen/x/v1"}},
		{ImportPath: "ex.com/m/gen/y/v1.test", Name: "main", Dir: filepath.Join(root, "gen", "y", "v1"), Deps: []string{"ex.com/m/gen/y/v1"}},
	}
	return func(context.Context, string, []string) (io.Reader, error) {
		pr, pw := io.Pipe()
		go func() {
			enc := json.NewEncoder(pw)
			for _, p := range pkgs {
				_ = enc.Encode(p)
			}
			_ = pw.Close()
		}()
		return pr, nil
	}
}

func TestAffectedRuleFields_Select(t *testing.T) {
	guard := rule{On: config.CIGateOnGoSource, Packages: []string{"./guard"}}
	for _, tc := range []struct {
		name    string
		changed []string
		rules   []rule
		want    string // packages, or "FULL: <reason fragment>"
	}{
		{"go-source runs the walker on any Go change", []string{"c/c.go"}, []rule{guard}, "ex.com/m/c ex.com/m/guard"},
		{"go-source ignores non-Go files", []string{"docs/x.md"}, []rule{guard, {Paths: []string{"docs/**"}, Packages: []string{"./a"}}}, "ex.com/m/a"},
		{"go-source does not claim a Go file outside every package", []string{"tools/x.go"}, []rule{guard}, "FULL: tools/x.go changed"},
		{"go-source honours except", []string{"c/c.go"}, []rule{{On: config.CIGateOnGoSource, Except: []string{"c/**"}, Packages: []string{"./guard"}}}, "ex.com/m/c"},
		{"packages_importing selects the helper's test importers", []string{"db/schema.sql"}, []rule{{Paths: []string{"db/**"}, PackagesImporting: []string{"ex.com/m/testutil"}}}, "ex.com/m/b"},
		{"packages_importing takes a ./dir", []string{"db/schema.sql"}, []rule{{Paths: []string{"db/**"}, PackagesImporting: []string{"./testutil"}}}, "ex.com/m/b"},
		{"{dir} maps a proto to its generated package", []string{"proto/x/v1/a.proto"}, []rule{{Paths: []string{"proto/**"}, Packages: []string{"./gen/{dir}"}}}, "ex.com/m/gen/x/v1"},
		{"{dir} per matched file", []string{"proto/x/v1/a.proto", "proto/y/v1/b.proto"}, []rule{{Paths: []string{"proto/**"}, Packages: []string{"./gen/{dir}"}}}, "ex.com/m/gen/x/v1 ex.com/m/gen/y/v1"},
		{"{dir} with a subtree", []string{"proto/x/a.proto"}, []rule{{Paths: []string{"proto/*/*.proto"}, Packages: []string{"./gen/{dir}/..."}}}, "ex.com/m/gen/x/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "f", changed: tc.changed}.run, GoList: fieldsListed(root), Rules: tc.rules})
			if frag, ok := strings.CutPrefix(tc.want, "FULL: "); ok {
				if !a.Full || !strings.Contains(a.Reason, frag) {
					t.Fatalf("full=%v reason %q, want full with %q", a.Full, a.Reason, frag)
				}
				return
			}
			if a.Full {
				t.Fatalf("fell back to the full suite: %s", a.Reason)
			}
			if got := strings.Join(a.Packages, " "); got != tc.want {
				t.Fatalf("packages = %q, want %q (selections %q)", got, tc.want, a.Selections)
			}
		})
	}
}

func TestExpandDir(t *testing.T) {
	for _, tc := range []struct{ pat, f, glob, want string }{
		{"./gen/{dir}", "proto/x/v1/a.proto", "proto/**", "./gen/x/v1"},
		{"./gen/{dir}", "proto/a.proto", "proto/**", "./gen"},
		{"./gen/{dir}", "api/x/a.proto", "*.proto", "./gen/api/x"},
		{"./{dir}/...", "api/x/a.proto", "api/*/*.proto", "./x/..."},
	} {
		if got := expandDir(tc.pat, tc.f, []string{tc.glob}); got != tc.want {
			t.Errorf("expandDir(%q, %q, %q) = %q, want %q", tc.pat, tc.f, tc.glob, got, tc.want)
		}
	}
}

func TestPlan_FullChecksRejectUnknownRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  string
		want bool
	}{
		{"path-gated extra", "electron", true},
		{"configured frontend", "test-web", true},
		{"typo", "electron-typo", false},
		{"missing frontend", "test-missing", false},
		{"non-path-gated row", UnitTests, false},
		{"frontend row shadowed by non-path-gated extra", "test-shadowed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.ProjectConfig{}
			cfg.CI.Gate.AffectedRules = []rule{{Paths: []string{"proto/**"}, FullChecks: []string{tc.row}}}
			cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{
				{Name: "electron", Command: []string{"echo", "ok"}, Paths: []string{"electron/**"}},
				{Name: "test-shadowed", Command: []string{"echo", "ok"}},
			}
			_, err := Plan(PlanInput{
				Root: t.TempDir(), Cfg: cfg, Forge: []string{"forge"},
				CI: templates.CIWorkflowData{Frontends: []templates.FrontendCIConfig{{Name: "web"}, {Name: "shadowed"}}},
			})
			if tc.want {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "affected_rules[0].full_checks[0]") || !strings.Contains(err.Error(), tc.row) {
				t.Fatalf("want full_checks error naming %q, got %v", tc.row, err)
			}
		})
	}
}

func TestAffectedRuleFields_FullChecksForceRowsWhole(t *testing.T) {
	root := webRoot(t, map[string]string{"test": "vitest run", RelatedScript: "vitest related --run --passWithNoTests"},
		"frontends/web/src/App.tsx")
	rules := []rule{
		{Paths: []string{"frontends/web/**"}, Frontends: []string{"web"}},
		{Paths: []string{"frontends/web/src/gen/**", "proto/**"}, FullChecks: []string{"test-web", "electron"}},
	}
	checks := append(unitAndDeps(), Check{Name: "electron", Lane: LaneChecks, Paths: []string{"electron/**"}})

	// Without the full_checks rule matching: related tests, electron N/A.
	a := affectedWith(t, root, fakeGit{branch: "f", changed: []string{"frontends/web/src/App.tsx"}}, rules...)
	got := WithFrontendTests(ApplyAffected(checks, a), a, root, []templates.FrontendCIConfig{webFE})
	if argv := strings.Join(find(t, got, "test-web").Argv, " "); !strings.Contains(argv, "test:related") {
		t.Fatalf("argv = %q, want related tests", argv)
	}
	if find(t, got, "electron").NotApplicable == "" {
		t.Fatal("electron ran with no change under its paths")
	}

	// A proto change: test-web is added and runs whole; electron runs.
	a = affectedWith(t, root, fakeGit{branch: "f", changed: []string{"frontends/web/src/App.tsx", "proto/x/v1/a.proto"}}, rules...)
	if a.Full || strings.Join(a.FullChecks, ",") != "electron,test-web" {
		t.Fatalf("full=%v (%s) full checks %v", a.Full, a.Reason, a.FullChecks)
	}
	got = WithFrontendTests(ApplyAffected(checks, a), a, root, []templates.FrontendCIConfig{webFE})
	fe := find(t, got, "test-web")
	if argv := strings.Join(fe.Argv, " "); strings.Contains(argv, "test:related") || !strings.Contains(argv, "run test") || !strings.Contains(fe.Note, "full_checks") {
		t.Errorf("argv = %q note %q, want the whole suite", argv, fe.Note)
	}
	if e := find(t, got, "electron"); e.NotApplicable != "" || !strings.Contains(e.Note, "full_checks") {
		t.Errorf("electron = %+v, want run whole", e)
	}

	// full_checks alone selects a frontend no rule's frontends named.
	a = affectedWith(t, root, fakeGit{branch: "f", changed: []string{"proto/x/v1/a.proto"}}, rules[1])
	got = WithFrontendTests(ApplyAffected(checks, a), a, root, []templates.FrontendCIConfig{webFE})
	if strings.Join(names(got), " ") != "deps-web unit-tests test-web flake-check electron" {
		t.Fatalf("rows = %v", names(got))
	}

	// Path-gating alone (the unit lane not narrowed) still honours it.
	a = ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "f", changed: []string{"proto/a.proto"}}.run, Rules: rules, ChangesOnly: true})
	if e := find(t, GateByPaths(checks, a), "electron"); e.NotApplicable != "" {
		t.Errorf("electron N/A under ChangesOnly: %s", e.NotApplicable)
	}
}
