package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeGateBudgetContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts/merge-gate-budgets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got gateBudgets
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := gateBudgets{Wall: 300, Steps: map[string]int{"plan": 25, "npm-r": 60, "npm-frontends-web": 60, "npm-tools-protoc-gen-es": 15, "generate": 60, "plan-2": 20, "build": 45, "test": 120, "typecheck": 120, "lint": 60, "proto": 15, "vet": 60, "migsafety": 10, "migorder": 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("budget changed: got %+v want %+v; tier slow checks into release, do not raise the 300s target", got, want)
	}
	// --amend critical path: plan, install, generate, replan, parallel readers.
	if got.Steps["plan"]+60+got.Steps["generate"]+got.Steps["plan-2"]+120 > got.Wall {
		t.Fatal("critical path budgets exceed 300s")
	}
}

func TestGateReportFailureFirstAndHealthNotPRBlame(t *testing.T) {
	dir := t.TempDir()
	log := "--- FAIL: TestExact/sub (package example/pkg)\n" + strings.Repeat("noise\n", 1000) + "\x1b[31m FAIL \x1b[0m src/a.test.ts > suite > exact name\n"
	os.WriteFile(filepath.Join(dir, "test.log"), []byte(log), 0600)
	os.WriteFile(filepath.Join(dir, "test.row"), []byte("test 1 301 0\n"), 0600)
	var out bytes.Buffer
	rc := runGateReport([]string{"-dir", dir, "-wall", "301", "-budgets", filepath.Join(repoRoot, "scripts/merge-gate-budgets.json")}, &out, &out)
	if rc != 0 {
		t.Fatalf("timing must not be PR failure: %d %s", rc, &out)
	}
	s := out.String()
	if !strings.HasPrefix(s, "--- FAIL: TestExact/sub (package example/pkg)\nFAIL src/a.test.ts > suite > exact name") {
		t.Fatalf("failure identity not first: %s", s)
	}
	if !strings.Contains(s, "requirement NOT MET") || !strings.Contains(s, "NOT PR blame") || strings.Contains(s, "noise") {
		t.Fatalf("bad health report: %s", s)
	}
}

func TestBudgetFilterFailureIncludesPackage(t *testing.T) {
	var out bytes.Buffer
	f := newBudgetFilter(0, nil, &out)
	f.Write([]byte(`{"Action":"fail","Package":"example/p","Test":"TestEarly/sub"}` + "\n"))
	if !strings.Contains(out.String(), "--- FAIL: TestEarly/sub (package example/p)") {
		t.Fatal(out.String())
	}
}

func TestMergeTierDefersReleaseOnlyLanes(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "internal/handlers/chat/rpc_get.go"}}, Go: GoPlan{LaneFull: selected("pin"), Packages: []string{"./internal/handlers/chat"}}, Web: WebPlan{LaneFull: selected("fallback"), Tests: []string{"src/x.test.ts"}}, Electron: ElectronPlan{LaneFull: selected("read")}, DocGen: DocGenPlan{LaneFull: selected("input")}, ForgeGen: ForgeGenPlan{LaneFull: selected("handler")}}
	mergeTier(pl)
	if pl.Go.Full || pl.Web.Full || pl.Electron.Full || pl.DocGen.Full || pl.ForgeGen.Full || len(pl.Web.Tests) != 0 {
		t.Fatalf("release lane leaked: %+v", pl)
	}
	if len(pl.Go.Packages) != 1 || len(pl.ReleaseDeferred) == 0 {
		t.Fatal("lost scoped tests or deferral accounting")
	}
}

func TestMergeTierCodegenInputs(t *testing.T) {
	for _, p := range []string{"proto/a.proto", "db/migrations/1.up.sql", "internal/foo/contract.go", "forge.yaml"} {
		pl := &Plan{Files: []FileReport{{Path: p}}, ForgeGen: ForgeGenPlan{LaneFull: selected("input")}}
		mergeTier(pl)
		if !pl.ForgeGen.Full {
			t.Errorf("missed input %s", p)
		}
	}
	for _, p := range []string{"gen/a.pb.go", "internal/handlers/chat/rpc_get_test.go", ".forge/hashes.json"} {
		pl := &Plan{Files: []FileReport{{Path: p}}, ForgeGen: ForgeGenPlan{LaneFull: selected("output")}}
		mergeTier(pl)
		if pl.ForgeGen.Full {
			t.Errorf("output selected as input: %s", p)
		}
	}
}

// Exercise the real gate's vet block with a classified no-test production
// command, intercepting go rather than compiling a nested module. This stays
// fast under -short and reads the script in-process for cache correctness.
func TestMergeVetIncludesProductionPackageWithoutTests(t *testing.T) {
	g := noWalkers()
	// An ordinary importer with no tests must be vetted as well as the
	// changed production package; neither appears in TestDeps.
	g.DirPkg["cmd/no_tests"] = mod + "/cmd/no_tests"
	g.BuildDeps[mod+"/cmd/no_tests"] = map[string]bool{mod + "/cmd/no_tests": true, mod + "/tools/docgen/cli": true}
	pl := ClassifyReach([]Change{{Path: "tools/docgen/cli/main.go"}}, g, ReachDirect)
	mergeTier(pl)
	if len(pl.Go.Packages) != 0 || !reflect.DeepEqual(pl.Go.Build, []string{"./cmd/no_tests", "./tools/docgen/cli"}) {
		t.Fatalf("fixture must select production build only: %+v", pl.Go)
	}
	dir := gateDir(t, pl, map[string]int{"build": 0, "test": 0, "proto": 0, "lint": 0})
	problems := strings.Join(verifyGate(filepath.Join(dir, "plan.json"), dir), ";")
	if !strings.Contains(problems, "vet never completed") {
		t.Fatalf("no-test production package passed without vet: %s", problems)
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "  mapfile -t vet_pkgs")
	if start < 0 {
		t.Fatal("vet dispatch block missing")
	}
	end := strings.Index(src[start:], "  if echo ")
	if end < 0 {
		t.Fatal("vet dispatch block end missing")
	}
	block := src[start : start+end]
	// jq and Bash execute the actual dispatch; go is a shell spy. No real
	// vet/build or dependency on external files hidden from the Go test cache.
	cmd := exec.Command("bash", "-c", `set -euo pipefail
run_plan=$1
quarter=2
pids=()
phase() { printf 'phase: %s\n' "$1"; shift; "$@"; }
env() { shift; "$@"; }
go() { printf 'go: %s\n' "$*"; }
`+block+`
for p in "${pids[@]}"; do wait "$p"; done`, "vet-dispatch", filepath.Join(dir, "plan.json"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vet dispatch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "phase: vet\ngo: vet ./cmd/no_tests ./tools/docgen/cli") {
		t.Fatalf("production command was not vetted: %s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "vet.row"), []byte("vet 0 1 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if problems := verifyGate(filepath.Join(dir, "plan.json"), dir); len(problems) > 0 {
		t.Fatalf("completed vet did not satisfy verifier: %v", problems)
	}
}

// The rebase onto #1698 must retain ee79d69f's evidence binding while
// applying merge-only policy to both the initial and regenerated plans.
func TestMergeTierPreservesLandedPlanIdentity(t *testing.T) {
	pl := &Plan{Schema: planSchema, Base: "base", Head: "head", InputDigest: strings.Repeat("a", 64)}
	mergeTier(pl)
	b, err := json.Marshal(pl)
	if err != nil {
		t.Fatal(err)
	}
	var saved Plan
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Merge || saved.Schema != planSchema || saved.Base != "base" || saved.Head != "head" || saved.InputDigest != pl.InputDigest {
		t.Fatalf("merge policy dropped plan identity: %+v", saved)
	}
	b, err = os.ReadFile(filepath.Join(repoRoot, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`phase plan go run ./tools/test-affected plan -merge -json`,
		`phase plan-2 go run ./tools/test-affected plan -merge -json`,
		`run -plan "$run_plan" -base "$base" -lanes go,web`,
		`gate-verify -plan "$run_plan" -base "$base"`,
		`load_selection || fail=1`,
		`regeneration expanded generator obligations`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("lost authoritative merge-plan consumer: %s", want)
		}
	}
}

func TestMergeTierCoverageAndWiring(t *testing.T) {
	read := func(p string) string {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(repoRoot, p))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	merge, release := read("scripts/merge-gate.sh"), read("scripts/release-gate.sh")
	for _, s := range []string{"plan -merge", "-lanes go,web -p", "phase proto task proto:lint", "phase vet", "gate-report", "summary.md"} {
		if !strings.Contains(merge, s) {
			t.Errorf("merge missing %s", s)
		}
	}
	for _, s := range []string{"task generate:docs:check", "task build:web:verify", "task test ;", "task test:integration", "task test:e2e", "go build ./...", "go vet ./...", "STEPS=(npm generate docs lint build webbuild", "missing_steps"} {
		if !strings.Contains(release, s) {
			t.Errorf("release missing %s", s)
		}
	}
	pl := &Plan{Merge: true, Go: GoPlan{Packages: []string{"./internal/x"}}}
	dir := gateDir(t, pl, map[string]int{"build": 0, "test": 0})
	problems := strings.Join(verifyGate(filepath.Join(dir, "plan.json"), dir), ";")
	if !strings.Contains(problems, "proto") || !strings.Contains(problems, "vet") {
		t.Fatalf("missing obliged checks: %s", problems)
	}
}
