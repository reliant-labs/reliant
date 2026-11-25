package cigate

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
)

type rule = config.CIGateAffectedRule

func affectedWith(t *testing.T, root string, g fakeGit, rules ...rule) Affected {
	t.Helper()
	return ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Rules: rules})
}

var webFE = templates.FrontendCIConfig{Name: "web", Path: "frontends/web", PackageManager: "npm"}

// unitAndDeps is the test lane as Plan writes it for one frontend.
func unitAndDeps() []Check {
	return []Check{
		{Name: "deps-web", Lane: LaneTest},
		{Name: UnitTests, Lane: LaneTest, Argv: []string{"task", "test"}},
		{Name: FlakeCheck, Lane: LaneTest},
	}
}

// webRoot writes a frontend whose package.json declares scripts, plus the
// source files a branch changed.
func webRoot(t *testing.T, scripts map[string]string, files ...string) string {
	t.Helper()
	root := t.TempDir()
	manifest, err := json.Marshal(map[string]any{"name": "web", "scripts": scripts})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "frontends/web/package.json", string(manifest))
	write(t, root, "frontends/web/package-lock.json", "{}")
	for _, f := range files {
		write(t, root, f, "x")
	}
	return root
}

func TestAffectedRules_ClaimedFrontendPathRunsItsRelatedTests(t *testing.T) {
	root := webRoot(t, map[string]string{"test": "vitest run", RelatedScript: "vitest related --run --passWithNoTests"},
		"frontends/web/src/App.tsx")
	a := affectedWith(t, root, fakeGit{branch: "f", changed: []string{"frontends/web/src/App.tsx", "frontends/web/src/gone.ts"}},
		rule{Paths: []string{"frontends/web/**"}, Frontends: []string{"web"}})
	if a.Full {
		t.Fatalf("a claimed frontend file fell back to the full suite: %s", a.Reason)
	}
	if len(a.Packages) != 0 || strings.Join(a.Frontends, ",") != "web" {
		t.Fatalf("packages %v frontends %v", a.Packages, a.Frontends)
	}
	if !strings.Contains(a.Line(), "frontend tests: web") || len(a.Selections) != 1 || !strings.Contains(a.Selections[0], "ci.gate.affected_rules[0] (frontends/web/**): 2 changed file(s) → frontend tests: web") {
		t.Errorf("line %q selections %q", a.Line(), a.Selections)
	}

	got := WithFrontendTests(ApplyAffected(unitAndDeps(), a), a, root, []templates.FrontendCIConfig{webFE})
	if strings.Join(names(got), " ") != "deps-web unit-tests test-web flake-check" {
		t.Fatalf("rows = %v", names(got))
	}
	if find(t, got, UnitTests).NotApplicable == "" {
		t.Error("no Go package reached: unit-tests is N/A, as without rules")
	}
	fe := find(t, got, "test-web")
	// npm needs `--` before a script's arguments; the deleted file is not passed.
	if argv := strings.Join(fe.Argv, " "); !strings.HasSuffix(argv, "run test:related -- src/App.tsx") {
		t.Errorf("argv = %q", argv)
	}
	if fe.Dir != "frontends/web" || fe.Lane != LaneTest || fe.Group != GroupTest || !fe.Heavy || strings.Join(fe.After, ",") != "deps-web" {
		t.Errorf("row = %+v", fe)
	}
}

// Only source modules go to test:related: a changed config file, manifest or
// lockfile can change every test while relating to none, so any one of them
// under the frontend runs the whole suite.
func TestAffectedRules_NonSourceChangeRunsTheWholeSuite(t *testing.T) {
	scripts := map[string]string{"test": "vitest run", RelatedScript: "vitest related --run --passWithNoTests"}
	for _, tc := range []struct {
		name    string
		changed []string
		related string // the argv suffix of a related run; "" means the whole suite
		note    string
	}{
		{"source only", []string{"src/App.tsx", "src/lib/util.ts", "src/styles/app.css"}, "run test:related -- src/App.tsx src/lib/util.ts src/styles/app.css", ""},
		{"test runner config", []string{"vitest.config.ts"}, "", "vitest.config.ts is not a source module"},
		{"build config", []string{"vite.config.ts"}, "", "vite.config.ts"},
		{"a *.config.* anywhere", []string{"src/tailwind.config.js"}, "", "src/tailwind.config.js"},
		{"a setup file in the root", []string{"vitest.setup.ts"}, "", "vitest.setup.ts"},
		{"tsconfig", []string{"tsconfig.app.json"}, "", "tsconfig.app.json"},
		{"package.json", []string{"package.json"}, "", "package.json"},
		{"lockfile", []string{"package-lock.json"}, "", "package-lock.json"},
		{"pnpm lockfile", []string{"pnpm-lock.yaml"}, "", "pnpm-lock.yaml"},
		{".npmrc", []string{".npmrc"}, "", ".npmrc"},
		{"a dotfile config with a source extension", []string{".eslintrc.cjs"}, "", ".eslintrc.cjs"},
		{"a JSON fixture under src", []string{"src/data.json"}, "", "src/data.json"},
		{"mixed source and config", []string{"src/App.tsx", "vitest.config.ts"}, "", "vitest.config.ts"},
		{"mixed source and lockfile", []string{"src/App.tsx", "package-lock.json"}, "", "package-lock.json"},
		// A source file named like a tool but not in the root is source.
		{"vite.ts under src", []string{"src/vite.ts"}, "run test:related -- src/vite.ts", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var files []string
			for _, f := range tc.changed {
				files = append(files, "frontends/web/"+f)
			}
			root := webRoot(t, scripts)
			for _, f := range files {
				if _, err := os.Stat(filepath.Join(root, f)); err != nil { // keep the fixture's package.json
					write(t, root, f, "x")
				}
			}
			a := affectedWith(t, root, fakeGit{branch: "f", changed: files},
				rule{Paths: []string{"frontends/web/**"}, Frontends: []string{"web"}})
			if a.Full {
				t.Fatalf("full suite: %s", a.Reason)
			}
			fe := find(t, WithFrontendTests(unitAndDeps(), a, root, []templates.FrontendCIConfig{webFE}), "test-web")
			argv := strings.Join(fe.Argv, " ")
			if tc.related != "" {
				if !strings.HasSuffix(argv, tc.related) {
					t.Errorf("argv = %q, want a related run ending %q", argv, tc.related)
				}
				return
			}
			// A pnpm lockfile makes pnpm the manager, which takes no `--`.
			if !strings.Contains(argv, "run test") || !strings.HasSuffix(argv, "--passWithNoTests") || strings.Contains(argv, RelatedScript) {
				t.Errorf("argv = %q, want the whole suite", argv)
			}
			if !strings.Contains(fe.Note, "whole suite") || !strings.Contains(fe.Note, tc.note) {
				t.Errorf("note = %q, want it to name %q", fe.Note, tc.note)
			}
		})
	}
}

func TestAffectedRules_FrontendWithoutRelatedScriptRunsItsWholeSuite(t *testing.T) {
	root := webRoot(t, map[string]string{"test": "vitest run"}, "frontends/web/src/App.tsx")
	a := affectedWith(t, root, fakeGit{branch: "f", changed: []string{"frontends/web/src/App.tsx"}},
		rule{Paths: []string{"frontends/web/**"}, Frontends: []string{"web"}})
	fe := find(t, WithFrontendTests(unitAndDeps(), a, root, []templates.FrontendCIConfig{webFE}), "test-web")
	if argv := strings.Join(fe.Argv, " "); !strings.HasSuffix(argv, "run test -- --passWithNoTests") {
		t.Errorf("argv = %q, want the whole test script as task test runs it", argv)
	}

	// A rule that selects the frontend for a file outside it (a proto its
	// hooks are generated from) has no file to relate: the whole suite.
	root = webRoot(t, map[string]string{"test": "vitest run", RelatedScript: "vitest related --run"})
	a = affectedWith(t, root, fakeGit{branch: "f", changed: []string{"proto/x.proto"}},
		rule{Paths: []string{"proto/**"}, Frontends: []string{"web"}})
	fe = find(t, WithFrontendTests(unitAndDeps(), a, root, []templates.FrontendCIConfig{webFE}), "test-web")
	if argv := strings.Join(fe.Argv, " "); !strings.HasSuffix(argv, "run test -- --passWithNoTests") {
		t.Errorf("argv = %q", argv)
	}

	// No test script at all: N/A, not a failure.
	root = webRoot(t, map[string]string{"build": "vite build"})
	fe = find(t, WithFrontendTests(unitAndDeps(), a, root, []templates.FrontendCIConfig{webFE}), "test-web")
	if fe.NotApplicable == "" {
		t.Errorf("row = %+v", fe)
	}
}

func TestAffectedRules_Packages(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name    string
		changed []string
		rules   []rule
		want    string
	}{
		{"a directory pattern", []string{"sql/q.sql"}, []rule{{Paths: []string{"sql/**"}, Packages: []string{"./c"}}}, "ex.com/m/c"},
		{"a subtree pattern", []string{"sql/q.sql"}, []rule{{Paths: []string{"sql/**"}, Packages: []string{"./..."}}}, "ex.com/m/a ex.com/m/b ex.com/m/c"},
		{"an import path pattern", []string{"sql/q.sql"}, []rule{{Paths: []string{"sql/**"}, Packages: []string{"ex.com/m/a/..."}}}, "ex.com/m/a"},
		{"a pattern with no tested package selects nothing", []string{"sql/q.sql"}, []rule{{Paths: []string{"sql/**"}, Packages: []string{"./nested/..."}}}, ""},
		// A Go file keeps its own mapping; the rule adds to it. A named
		// package's importers are not added (b imports a; c is named).
		{"a rule adds to a Go file's packages", []string{"b/b.go"}, []rule{{Paths: []string{"b/**"}, Packages: []string{"./c"}}}, "ex.com/m/b ex.com/m/c"},
		{"an ignored file a rule claims still selects", []string{"docs/a.md"}, []rule{{Paths: []string{"docs/**"}, Packages: []string{"./c"}}}, "ex.com/m/c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "f", changed: tc.changed}.run, GoList: listed(root), Rules: tc.rules, Ignore: []string{"*.md"}})
			if a.Full {
				t.Fatalf("full suite: %s", a.Reason)
			}
			if got := strings.Join(a.Packages, " "); got != tc.want {
				t.Fatalf("packages = %q, want %q", got, tc.want)
			}
		})
	}
	a := affectedWith(t, root, fakeGit{branch: "f", changed: []string{"sql/q.sql"}}, rule{Paths: []string{"sql/**"}, Packages: []string{"./nested/..."}})
	if len(a.Selections) != 1 || !strings.Contains(a.Selections[0], "./nested/... (no package with tests)") {
		t.Errorf("selections = %q", a.Selections)
	}
}

func TestAffectedRules_FullAndUnmatched(t *testing.T) {
	root := t.TempDir()
	kcl := rule{Paths: []string{"deploy/kcl/**"}, Full: true, Reason: "KCL is read by the render tests"}
	web := rule{Paths: []string{"frontends/web/**"}, Except: []string{"frontends/web/README.txt"}, Frontends: []string{"web"}}
	for _, tc := range []struct {
		name    string
		changed []string
		why     string
	}{
		{"a full rule", []string{"b/b.go", "deploy/kcl/dev/main.k"}, "deploy/kcl/dev/main.k changed — KCL is read by the render tests (ci.gate.affected_rules[0])"},
		{"a path no rule matches behaves as today", []string{"frontends/web/a.ts", "scripts/x.sh"}, "scripts/x.sh changed, and it is not Go source"},
		{"except unclaims a file", []string{"frontends/web/README.txt"}, "frontends/web/README.txt changed, and it is not Go source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := affectedWith(t, root, fakeGit{branch: "f", changed: tc.changed}, kcl, web)
			if !a.Full || !strings.Contains(a.Reason, tc.why) {
				t.Fatalf("Full=%v Reason=%q, want %q", a.Full, a.Reason, tc.why)
			}
			if len(a.Frontends) != 0 || len(a.Changed) != len(tc.changed) {
				t.Errorf("full suite after the diff: frontends %v, changed %v (kept for path-gated checks)", a.Frontends, a.Changed)
			}
			// Full mode adds no frontend rows: task test runs every suite.
			if got := names(WithFrontendTests(unitAndDeps(), a, root, []templates.FrontendCIConfig{webFE})); strings.Join(got, " ") != "deps-web unit-tests flake-check" {
				t.Errorf("rows = %v", got)
			}
		})
	}
}

// Without rules nothing is added: the same rows, the same line.
func TestAffectedRules_NoneIsToday(t *testing.T) {
	root := t.TempDir()
	a := affectedWith(t, root, fakeGit{branch: "f", changed: []string{"a/a.go"}})
	if a.Full || len(a.Frontends) != 0 || len(a.Selections) != 0 {
		t.Fatalf("%+v", a)
	}
	want := "unit tests: affected mode — 2 of 3 test packages reach the 1 changed file(s) since origin/main (0123456789ab); CI_FULL=1 runs the full suite"
	if a.Line() != want {
		t.Errorf("Line =\n %q\nwant\n %q", a.Line(), want)
	}
	if got := names(WithFrontendTests(ApplyAffected(unitAndDeps(), a), a, root, []templates.FrontendCIConfig{webFE})); strings.Join(got, " ") != "deps-web unit-tests flake-check" {
		t.Errorf("rows = %v", got)
	}
}

func TestGateByPaths(t *testing.T) {
	electron := Check{Name: "electron", Paths: []string{"electron/**"}, Argv: []string{"npm", "test"}}
	plain := check("lint", "lint")
	branch := func(changed ...string) Affected {
		return Affected{BaseRef: "origin/main", Base: "0123", Changed: changed}
	}

	got := GateByPaths([]Check{electron, plain}, branch("a/a.go"))
	if na := got[0].NotApplicable; !strings.Contains(na, "affected mode: no change under electron/**") {
		t.Errorf("unmatched: N/A = %q", na)
	}
	if got[1].NotApplicable != "" || electron.NotApplicable != "" {
		t.Error("an ungated check is untouched, and the input is not modified")
	}

	got = GateByPaths([]Check{electron}, branch("electron/main.ts", "electron/preload.ts"))
	if got[0].NotApplicable != "" || got[0].Note != "affected mode: electron/main.ts and 1 more changed" {
		t.Errorf("matched: %+v", got[0])
	}

	// The unit lane's own full fallback still judges gated checks by the diff.
	full := branch("go.mod")
	full.Full, full.Reason = true, "go.mod changed"
	if got = ApplyAffected([]Check{electron}, full); got[0].NotApplicable == "" {
		t.Error("an unclaimed go.mod change does not run the electron tests")
	}

	// Full mode (CI_FULL, the default branch, a full tier): always runs.
	got = GateByPaths([]Check{electron}, Affected{Full: true, Reason: "CI_FULL=1"})
	if got[0].NotApplicable != "" || got[0].Note != "" {
		t.Errorf("full mode: %+v", got[0])
	}
}

// An N/A row is never cached: only a PASS a check earned is recorded.
func TestGateByPaths_NAIsNotRecorded(t *testing.T) {
	rec := &recordingVerdicts{}
	got := GateByPaths([]Check{{Name: "electron", Paths: []string{"electron/**"}, Argv: []string{"false"}}}, Affected{BaseRef: "origin/main", Changed: []string{"a/a.go"}})
	rep := Run(context.Background(), "test", got, Options{Out: io.Discard, Root: t.TempDir(), Verdicts: rec})
	if len(rep.Results) != 1 || rep.Results[0].Status != NA || rep.Failed() {
		t.Fatalf("results = %+v", rep.Results)
	}
	if len(rec.recorded) != 0 {
		t.Errorf("recorded %v", rec.recorded)
	}
}

type recordingVerdicts struct{ recorded []string }

func (r *recordingVerdicts) Lookup(Check) (string, bool) { return "", false }
func (r *recordingVerdicts) Record(c Check)              { r.recorded = append(r.recorded, c.Name) }

func TestPlan_AffectedRulesAndPaths(t *testing.T) {
	in := PlanInput{Root: t.TempDir(), Forge: []string{"forge"},
		CI: templates.CIWorkflowData{HasFrontends: true, Frontends: []templates.FrontendCIConfig{webFE}}}
	in.Cfg = &config.ProjectConfig{}
	in.Cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{{Name: "electron", Command: []string{"npm", "test"}, Lane: "test", Paths: []string{"electron/**"}}}
	in.Cfg.CI.Gate.AffectedRules = []rule{{Paths: []string{"frontends/web/**"}, Frontends: []string{"web"}}}
	all, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if p := find(t, all, "electron").Paths; strings.Join(p, ",") != "electron/**" {
		t.Errorf("paths = %v", p)
	}

	in.Cfg.CI.Gate.AffectedRules = []rule{{Paths: []string{"frontends/admin/**"}, Frontends: []string{"admin"}}}
	if _, err := Plan(in); err == nil || !strings.Contains(err.Error(), `ci.gate.affected_rules[0]: frontends names "admin", which is not a frontend of this project (its frontends: web)`) {
		t.Errorf("err = %v", err)
	}
}

// A migration is embedded by the generated db/embed_gen.go, so a changed one
// maps to the db package and every test binary importing it, with no rule.
func TestComputeAffected_MigrationThroughTheEmbed(t *testing.T) {
	root := t.TempDir()
	pkgs := []listedPackage{
		{ImportPath: "ex.com/m/db", Name: "db", Dir: filepath.Join(root, "db"), EmbedFiles: []string{"migrations/001_a.up.sql"}},
		{ImportPath: "ex.com/m/store", Name: "store", Dir: filepath.Join(root, "store"), Deps: []string{"ex.com/m/db"}},
		{ImportPath: "ex.com/m/web", Name: "web", Dir: filepath.Join(root, "web")},
		{ImportPath: "ex.com/m/store.test", Name: "main", Dir: filepath.Join(root, "store"), Deps: []string{"ex.com/m/db", "ex.com/m/store [ex.com/m/store.test]"}},
		{ImportPath: "ex.com/m/web.test", Name: "main", Dir: filepath.Join(root, "web"), Deps: []string{"ex.com/m/web [ex.com/m/web.test]"}},
	}
	list := func(context.Context, string, []string) (io.Reader, error) {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		for _, p := range pkgs {
			_ = enc.Encode(p)
		}
		return strings.NewReader(b.String()), nil
	}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: fakeGit{branch: "f", changed: []string{"db/migrations/001_a.up.sql"}}.run, GoList: list})
	if a.Full {
		t.Fatalf("a migration fell back to the full suite: %s", a.Reason)
	}
	if got := strings.Join(a.Packages, " "); got != "ex.com/m/store" {
		t.Fatalf("packages = %q, want the db package's importers", got)
	}
}

// The same over a real module: git and `go list` see the embed.
func TestComputeAffected_MigrationRealModule(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git and `go list -deps -test` subprocesses; runs in task test")
	}
	root := gitRepo(t, map[string]string{
		"go.mod":                        "module ex.com/m\n\ngo 1.22\n",
		"db/embed_gen.go":               "package db\n\nimport \"embed\"\n\n//go:embed migrations/*.sql\nvar Migrations embed.FS\n",
		"db/migrations/001_a.up.sql":    "create table a (id int);\n",
		"store/store.go":                "package store\n\nimport \"ex.com/m/db\"\n\nvar M = db.Migrations\n",
		"store/store_test.go":           "package store\n\nimport \"testing\"\n\nfunc TestS(t *testing.T) { _ = M }\n",
		"other/other.go":                "package other\n",
		"other/other_test.go":           "package other\n\nimport \"testing\"\n\nfunc TestO(t *testing.T) {}\n",
		"db/migrations/000_keep.up.sql": "select 1;\n",
	})
	git(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, root, "checkout", "-q", "-b", "feature")
	write(t, root, "db/migrations/002_b.up.sql", "create table b (id int);\n")
	env := goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Env: env})
	if a.Full {
		t.Fatalf("full suite: %s", a.Reason)
	}
	if got := strings.Join(a.Packages, " "); got != "ex.com/m/store" {
		t.Fatalf("packages = %q, want ex.com/m/store", got)
	}
}
