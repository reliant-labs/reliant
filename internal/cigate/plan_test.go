package cigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(cs []Check) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func find(t *testing.T, cs []Check, name string) Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %v", name, names(cs))
	return Check{}
}

func serviceInput(t *testing.T) PlanInput {
	root := t.TempDir()
	write(t, root, "cmd/app/main.go", "package main\nfunc main(){}\n")
	write(t, root, "db/migrations/001_a.up.sql", "select 1;")
	return PlanInput{
		Root:  root,
		Forge: []string{"forge"},
		CI: templates.CIWorkflowData{
			HasServices: true, HasFrontends: true,
			Frontends:    []templates.FrontendCIConfig{{Name: "web", Path: "frontends/web"}},
			LintGolangci: true, LintBuf: true, LintMigrationSafety: true,
			LintFrontend: true, LintFrontendStyles: true,
			VulnGo: true, VulnNPM: true, LicenseCheck: true,
			HasKCL: true, VerifyGenerated: true, RunsBufGenerate: true, TestRace: true,
		},
	}
}

// The order is load-bearing: verify-generated is listed first (after
// dependency installs), and the KCL checks run last.
func TestPlan_OrderAndLanes(t *testing.T) {
	all, err := Plan(serviceInput(t))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(all), " ")
	want := "deps-web verify-generated golangci-lint buf-lint migration-safety lint-web typecheck-web css-health-web go-build build-web vuln-go vuln-npm license-check validate-kcl deployability deps-web unit-tests integration-tests"
	if got != want {
		t.Errorf("plan order:\n got %s\nwant %s", got, want)
	}

	if checks := names(Select(all, []Lane{LaneChecks})); checks[len(checks)-1] != "deployability" || checks[1] != "verify-generated" {
		t.Errorf("checks lane = %v", checks)
	}
	if test := names(Select(all, []Lane{LaneTest})); strings.Join(test, " ") != "deps-web unit-tests" {
		t.Errorf("test lane = %v", test)
	}
}

// `task ci` runs checks then test: the frontend install appears in both lanes
// and must run once, not twice.
func TestSelect_DedupesSharedRows(t *testing.T) {
	all, _ := Plan(serviceInput(t))
	n := 0
	for _, c := range Select(all, []Lane{LaneChecks, LaneTest, LaneIntegration}) {
		if c.Name == "deps-web" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("deps-web appears %d times in `task ci`, want 1", n)
	}
}

// The gate follows the same toggles the workflow renders from: a check the
// project turned off in forge.yaml is not a row at all (not a SKIPPED one).
func TestPlan_FollowsTheWorkflowToggles(t *testing.T) {
	in := serviceInput(t)
	in.CI.LintGolangci, in.CI.VulnGo, in.CI.LicenseCheck, in.CI.HasKCL, in.CI.HasFrontends, in.CI.Frontends = false, false, false, false, false, nil
	all, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"golangci-lint", "vuln-go", "license-check", "validate-kcl", "deployability", "deps-web", "lint-web", "build-web", "vuln-npm"} {
		for _, n := range names(all) {
			if n == gone {
				t.Errorf("%s is planned though its toggle is off", gone)
			}
		}
	}
	find(t, all, "go-build")
}

// A project with nothing for a check to look at gets N/A, which is not a skip.
func TestPlan_NotApplicableRows(t *testing.T) {
	in := PlanInput{Root: t.TempDir(), Forge: []string{"forge"}, CI: templates.CIWorkflowData{LintMigrationSafety: true, VerifyGenerated: true}}
	all, _ := Plan(in)
	if c := find(t, all, "migration-safety"); c.NotApplicable == "" {
		t.Error("migration-safety without db/migrations should be N/A")
	}
	if c := find(t, all, "go-build"); c.NotApplicable == "" {
		t.Error("go-build without cmd/ should be N/A")
	}
	if c := find(t, all, "integration-tests"); c.NotApplicable == "" {
		t.Error("integration-tests without //go:build integration files should be N/A")
	}
}

func TestHasIntegrationTests(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/a_test.go", "package a\n")
	write(t, root, "node_modules/x/x_test.go", "//go:build integration\n\npackage x\n")
	if HasIntegrationTests(root) {
		t.Fatal("a plain test, and one inside node_modules, are not an integration lane")
	}
	write(t, root, "internal/db/db_test.go", "//go:build integration\n\npackage db\n")
	if !HasIntegrationTests(root) {
		t.Fatal("a //go:build integration test file was not found")
	}
	root2 := t.TempDir()
	write(t, root2, "x_test.go", "//go:build linux && integration && !race\n\npackage x\n")
	if !HasIntegrationTests(root2) {
		t.Error("a compound constraint naming integration was not found")
	}
}

// A missing dependency directory makes the npm rows SKIPPED with the command
// that fixes it, never a bare npm failure that reads like a code defect.
func TestPlan_FrontendRowsNeedTheirDeps(t *testing.T) {
	in := serviceInput(t)
	all, _ := Plan(in)
	lint := find(t, all, "lint-web")
	reason := lint.Prereq()
	if !strings.Contains(reason, "npm ci") || !strings.Contains(reason, "frontends/web") {
		t.Errorf("reason %q should name the fix", reason)
	}
	write(t, in.Root, "frontends/web/node_modules/.keep", "")
	if reason := lint.Prereq(); reason != "" {
		t.Errorf("with node_modules present the row should run, got %q", reason)
	}
	if note := find(t, all, "deps-web").Done(); note != "already installed" {
		t.Errorf("deps-web Done = %q", note)
	}
}

// license-check exempts the project's own module (#755): go-licenses checks
// every package it loads, so without --ignore a project with no LICENSE file
// logged "Failed to find license" for each of its own packages.
func TestPlan_LicenseCheckIgnoresTheProjectsOwnModule(t *testing.T) {
	in := serviceInput(t)
	write(t, in.Root, "go.mod", "module example.com/shop\n\ngo 1.24\n\nrequire github.com/google/uuid v1.6.0\n")
	all, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(find(t, all, "license-check").Argv, " ")
	if want := "go-licenses check ./... --disallowed_types=forbidden,restricted --ignore example.com/shop"; got != want {
		t.Errorf("license-check argv:\n got %s\nwant %s", got, want)
	}
}

// go-licenses matches --ignore as a plain string prefix, so the ignores must
// not reach past the project's own modules, and must reach all of them.
func TestLicenseIgnores(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no go.mod: nothing to exempt", nil, ""},
		{"own module", map[string]string{
			"go.mod": "module example.com/shop\n",
		}, "example.com/shop"},
		{"a dependency extends the module path: only the subtree", map[string]string{
			"go.mod": "module example.com/shop\n\nrequire (\n\texample.com/shop-client v1.0.0\n\texample.com/shop/api v0.1.0\n)\n",
		}, "example.com/shop/"},
		{"workspace: modules inside the tree, not a linked checkout outside it", map[string]string{
			"go.mod":       "module example.com/shop\n",
			"go.work":      "go 1.24\n\nuse (\n\t.\n\t./tools\n\t./api\n\t../forge\n)\n",
			"tools/go.mod": "module example.com/shoptools\n",
			"api/go.mod":   "module example.com/shop/api\n", // nested: covered by example.com/shop
		}, "example.com/shop example.com/shoptools"},
		{"workspace modules requiring each other are not collisions", map[string]string{
			"go.mod":       "module example.com/shop\n\nrequire example.com/shoptools v0.0.0\n",
			"go.work":      "go 1.24\n\nuse (\n\t.\n\t./tools\n)\n",
			"tools/go.mod": "module example.com/shoptools\n",
		}, "example.com/shop example.com/shoptools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tc.files {
				write(t, root, rel, body)
			}
			if got := strings.Join(licenseIgnores(root), " "); got != tc.want {
				t.Errorf("licenseIgnores = %q, want %q", got, tc.want)
			}
		})
	}
}

// ci.race=false in forge.yaml reaches `task test` the way the workflow carries
// it, so the local gate and CI run the same go test flags.
func TestPlan_CarriesTheRaceToggleToTask(t *testing.T) {
	in := serviceInput(t)
	in.CI.TestRace = false
	all, _ := Plan(in)
	if got := strings.Join(find(t, all, "unit-tests").Argv, " "); got != "task test GOTESTRACE=" {
		t.Errorf("argv = %q", got)
	}
}

func TestPlan_ExtraChecks(t *testing.T) {
	in := serviceInput(t)
	in.Cfg = &config.ProjectConfig{}
	in.Cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{
		{Name: "gen-llm-gateway", Command: []string{"go", "run", "./cmd/gen-llm-gateway", "-check"}, Requires: []string{"definitely-not-a-binary-xyz"}},
	}
	all, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	extra := find(t, all, "gen-llm-gateway")
	if extra.Lane != LaneChecks {
		t.Errorf("extra check lane = %s, want checks (so `task ci:checks` and the CI checks job run it)", extra.Lane)
	}
	if got := names(Select(all, []Lane{LaneChecks})); got[len(got)-1] != "gen-llm-gateway" {
		t.Errorf("extras run after forge's own checks, got %v", got)
	}
	if len(extra.Requires) != 1 || extra.Group != GroupExtras || extra.Heavy || extra.MutatesTree || extra.NoCache {
		t.Errorf("extra check defaults: %+v", extra)
	}

	for name, bad := range map[string]config.CIGateCheck{
		"no name":           {Command: []string{"x"}},
		"no command":        {Name: "x"},
		"collides w/ forge": {Name: "golangci-lint", Command: []string{"x"}},
	} {
		in.Cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{bad}
		if _, err := Plan(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	in.Cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{{Name: "dup", Command: []string{"x"}}, {Name: "dup", Command: []string{"y"}}}
	if _, err := Plan(in); err == nil {
		t.Error("duplicate extra names must be rejected: CI_SKIP could not tell them apart")
	}
}

// The extras schema is what lets a project with its own gate (control-plane's
// ~26-step ci-gate.sh) move onto `forge ci gate`: every scheduling fact a
// step has must be expressible.
func TestPlan_ExtraChecksSchema(t *testing.T) {
	in := serviceInput(t)
	in.Cfg = &config.ProjectConfig{}
	no := false
	in.Cfg.CI.Gate.ExtraChecks = []config.CIGateCheck{
		{Name: "toolchain", Command: []string{"scripts/ci-step.sh", "toolchain"}, Group: "always"},
		{Name: "kcl-kata-prepull", Command: []string{"./scripts/test-kata-prepull.sh"}, Group: "kcl", Heavy: true, Requires: []string{"yq", "jq"}},
		{Name: "migration-chain", Command: []string{"scripts/ci-step.sh", "migration-chain"}, Heavy: true,
			Probe: &config.CIGateProbe{Command: []string{"docker", "info"}, Reason: "docker daemon not reachable"}},
		{Name: "reliant-vendored-proto", Command: []string{"scripts/ci-step.sh", "reliant-vendored-proto"}, Cache: &no, SkipExitCode: 77, Env: map[string]string{"B": "2", "A": "1"}},
		{Name: "pre-commit", Command: []string{"pre-commit", "run", "--all-files"}, MutatesTree: true, After: []string{"deps-web"}},
		{Name: "gen-llm-gateway", Command: []string{"go", "run", "./cmd/gen-llm-gateway", "-check"}, After: []string{"verify-generated"}},
		{Name: "image-control-plane", Command: []string{"docker", "build", "."}, Lane: "docker", Heavy: true},
		{Name: "lint-go", Command: []string{"scripts/ci-step.sh", "lint-go"}, Replaces: "golangci-lint", Heavy: true},
		{Name: "integration", Command: []string{"task", "test:integration"}, Replaces: "integration-tests"},
	}
	all, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if c := find(t, all, "kcl-kata-prepull"); c.Group != "kcl" || !c.Heavy || c.Lane != LaneChecks {
		t.Errorf("kcl-kata-prepull = %+v", c)
	}
	if c := find(t, all, "migration-chain"); strings.Join(c.Probe, " ") != "docker info" || c.ProbeReason != "docker daemon not reachable" {
		t.Errorf("probe not carried: %+v", c)
	}
	if c := find(t, all, "reliant-vendored-proto"); !c.NoCache || c.SkipExitCode != 77 || strings.Join(c.Env, " ") != "A=1 B=2" {
		t.Errorf("cache:false / skip_exit_code / env: %+v", c)
	}
	if c := find(t, all, "pre-commit"); !c.MutatesTree || c.OnFail == nil || c.After[0] != "deps-web" {
		t.Errorf("a tree-mutating extra needs its reset and its dependency: %+v", c)
	}
	if c := find(t, all, "image-control-plane"); c.Lane != "docker" {
		t.Errorf("custom lane = %q", c.Lane)
	}
	if lanes := Lanes(all); len(lanes) != 4 || lanes[3] != "docker" {
		t.Errorf("Lanes = %v", lanes)
	}
	for _, c := range Select(all, []Lane{LaneChecks, LaneTest, LaneIntegration}) {
		if c.Lane == "docker" {
			t.Error("a lane of the project's own naming must not run in `all`")
		}
	}
	// A replacement takes the replaced row's position, lane and group.
	checks := names(Select(all, []Lane{LaneChecks}))
	if checks[2] != "lint-go" {
		t.Errorf("lint-go should sit where golangci-lint was: %v", checks)
	}
	for _, n := range names(all) {
		if n == "golangci-lint" || n == "integration-tests" {
			t.Errorf("%s was replaced but still planned", n)
		}
	}
	if c := find(t, all, "integration"); c.Lane != LaneIntegration || c.Group != GroupIntegration {
		t.Errorf("replacement did not inherit lane/group: %+v", c)
	}

	for name, bad := range map[string][]config.CIGateCheck{
		"after names nothing":  {{Name: "x", Command: []string{"x"}, After: []string{"nope"}}},
		"cycle":                {{Name: "a", Command: []string{"x"}, After: []string{"b"}}, {Name: "b", Command: []string{"x"}, After: []string{"a"}}},
		"replaces nothing":     {{Name: "x", Command: []string{"x"}, Replaces: "nope"}},
		"replaced twice":       {{Name: "x", Command: []string{"x"}, Replaces: "go-build"}, {Name: "y", Command: []string{"x"}, Replaces: "go-build"}},
		"lane all":             {{Name: "x", Command: []string{"x"}, Lane: "all"}},
		"lane with spaces":     {{Name: "x", Command: []string{"x"}, Lane: "my lane"}},
		"empty probe":          {{Name: "x", Command: []string{"x"}, Probe: &config.CIGateProbe{}}},
		"skip exit code range": {{Name: "x", Command: []string{"x"}, SkipExitCode: 300}},
	} {
		in.Cfg.CI.Gate.ExtraChecks = bad
		if _, err := Plan(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// Forge's own rows carry the scheduling facts the gate runs them by.
func TestPlan_SchedulingOfForgesOwnChecks(t *testing.T) {
	all, err := Plan(serviceInput(t))
	if err != nil {
		t.Fatal(err)
	}
	vg := find(t, all, "verify-generated")
	// It regenerates a scratch copy, never the tree, so it neither runs
	// alone nor resets anything; it still needs the frontend's protoc-gen-es.
	if vg.MutatesTree || vg.Prereq != nil || vg.OnFail != nil || !vg.Heavy || strings.Join(vg.After, ",") != "deps-web" {
		t.Errorf("verify-generated is a heavy, non-mutating check after the frontend install: %+v", vg)
	}
	for _, n := range []string{"lint-web", "typecheck-web", "build-web"} {
		if c := find(t, all, n); strings.Join(c.After, ",") != "deps-web" {
			t.Errorf("%s must wait for its frontend's install, after = %v", n, c.After)
		}
	}
	for _, n := range []string{"buf-lint", "migration-safety", "vuln-npm", "css-health-web"} {
		if find(t, all, n).Heavy {
			t.Errorf("%s is light: it should not queue for a host slot", n)
		}
	}
	for _, n := range []string{"vuln-go", "vuln-npm", "deps-web"} {
		if !find(t, all, n).NoCache {
			t.Errorf("%s depends on state outside the tree and must not be cached", n)
		}
	}
	if find(t, all, "validate-kcl").Group != GroupKCL || find(t, all, UnitTests).Group != GroupTest {
		t.Error("groups for CI_ONLY")
	}
}

// A lockfile newer than the install means the install is stale.
func TestDepsCurrent(t *testing.T) {
	dir := t.TempDir()
	if depsCurrent(dir) {
		t.Fatal("no node_modules is not current")
	}
	write(t, dir, "node_modules/.package-lock.json", "{}")
	if !depsCurrent(dir) {
		t.Fatal("node_modules with no lockfile to compare against is current")
	}
	write(t, dir, "package-lock.json", "{}")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "node_modules", ".package-lock.json"), old, old); err != nil {
		t.Fatal(err)
	}
	if depsCurrent(dir) {
		t.Fatal("an install older than package-lock.json is stale")
	}
}

// buf is pinned only where verify-generated regenerates with it.
func TestPlan_BufPinnedOnlyWhereGenerateRunsIt(t *testing.T) {
	in := serviceInput(t)
	in.CI.RunsBufGenerate = false
	all, _ := Plan(in)
	if len(find(t, all, "verify-generated").Tools) != 0 {
		t.Error("verify-generated pins buf though generate does not run it")
	}
	in.CI.RunsBufGenerate = true
	all, _ = Plan(in)
	if tools := find(t, all, "verify-generated").Tools; len(tools) != 1 || tools[0].Binary != "buf" {
		t.Errorf("verify-generated tools = %+v, want buf", tools)
	}
}

// With one main package under cmd/, `go build ./cmd/...` would write the
// binary into the project root; the gate discards it instead. With several,
// go build discards them itself, and -o to a file would be an error.
func TestPlan_GoBuildWritesNoBinaryIntoTheTree(t *testing.T) {
	in := serviceInput(t) // cmd/app/main.go
	all, _ := Plan(in)
	if got := strings.Join(find(t, all, "go-build").Argv, " "); !strings.Contains(got, "-o "+os.DevNull) {
		t.Errorf("one binary: argv %q must discard the output", got)
	}
	write(t, in.Root, "cmd/tool/main.go", "package main\nfunc main(){}\n")
	write(t, in.Root, "cmd/internal/lib/lib.go", "package lib\n")
	all, _ = Plan(in)
	if got := strings.Join(find(t, all, "go-build").Argv, " "); strings.Contains(got, "-o") {
		t.Errorf("two binaries: argv %q must not name an output file", got)
	}
}
