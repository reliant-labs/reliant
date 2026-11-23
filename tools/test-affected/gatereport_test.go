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
	want := gateBudgets{Wall: 300, Steps: map[string]int{"plan": 25, "pnpm-frontends-web": 60, "pnpm-electron": 60, "pnpm-tools-protoc-gen-es": 15, "pgtest": 15, "generate": 60, "prewarm": 60, "docs": 60, "plan-2": 20, "build": 45, "test": 120, "typecheck": 120, "demo": 150, "webbuild": 150, "golangci": 120, "lint": 60, "proto": 15, "vet": 60, "migsafety": 10, "closedsets": 10, "migorder": 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("budget changed: got %+v want %+v; fix slow checks or move them to the release gate; do not raise the 300s target", got, want)
	}
	// --amend critical path: plan, install, the pgtest preflight, generate,
	// replan, parallel readers.
	if got.Steps["plan"]+60+got.Steps["pgtest"]+got.Steps["generate"]+got.Steps["plan-2"]+120 > got.Wall {
		t.Fatal("critical path budgets exceed the wall target")
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
	if !strings.Contains(s, "target NOT MET") || !strings.Contains(s, "NOT PR blame") || strings.Contains(s, "noise") {
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
	if len(pl.Go.Packages) != 0 || !reflect.DeepEqual(pl.Go.Build, []string{"./cmd/no_tests", "./tools/docgen/cli"}) {
		t.Fatalf("fixture must select production build only: %+v", pl.Go)
	}
	dir := gateDir(t, pl, map[string]int{"build": 0, "test": 0, "proto": 0, "lint": 0, "docs": 0, "golangci": 0})
	problems := strings.Join(verifyGate(filepath.Join(dir, "plan.json"), dir), ";")
	if !strings.Contains(problems, "vet never completed") {
		t.Fatalf("no-test production package passed without vet: %s", problems)
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "  if [ \"$gofull\" = true ]; then vet_pkgs")
	if start < 0 {
		t.Fatal("vet dispatch block missing")
	}
	end := strings.Index(src[start:], "  if echo ")
	if end < 0 {
		t.Fatal("vet dispatch block end missing")
	}
	block := src[start : start+end]
	// The block delegates to scripts/vet.sh; read it here too so an edit to
	// it invalidates this test's cache entry.
	vetScript, err := os.ReadFile(filepath.Join(repoRoot, "scripts/vet.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vetScript), `go vet "$@"`) || !strings.Contains(string(vetScript), `go vet -vettool=`) {
		t.Fatalf("scripts/vet.sh no longer runs go vet and the model vettool:\n%s", vetScript)
	}
	// jq and Bash execute the actual dispatch; go is a shell spy. No real
	// vet/build or dependency on external files hidden from the Go test cache.
	cmd := exec.Command("bash", "-c", `set -euo pipefail
run_plan=$1
gofull=false
queue=0
skip_lint=0
skip_vet=0
quarter=2
pids=()
phase() { printf 'phase: %s\n' "$1"; shift; "$@"; }
env() { shift; "$@"; }
go() { printf 'go: %s\n' "$*"; }
export -f go
`+block+`
for p in "${pids[@]}"; do wait "$p"; done`, "vet-dispatch", filepath.Join(dir, "plan.json"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vet dispatch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "phase: vet\ngo: vet ./cmd/no_tests ./tools/docgen/cli\n") ||
		!strings.Contains(string(out), "/modelvet ./cmd/no_tests ./tools/docgen/cli\n") {
		t.Fatalf("production command was not vetted: %s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "vet.row"), []byte("vet 0 1 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if problems := verifyGate(filepath.Join(dir, "plan.json"), dir); len(problems) > 0 {
		t.Fatalf("completed vet did not satisfy verifier: %v", problems)
	}
}

func TestMergeGateCoverageAndWiring(t *testing.T) {
	read := func(p string) string {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(repoRoot, p))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	merge, release := read("scripts/merge-gate.sh"), read("scripts/release-gate.sh")
	for _, s := range []string{"plan -json", "-lanes \"$test_lanes\" -web-full reached -p", "phase demo demo_build", "--optional-deps-guard", "phase docs gen_docs", "task generate:docs", "phase proto env CI_BASE=\"$base\" CI_QUEUE_ENTRY=\"$queue\" bash -c 'task proto:lint && scripts/check-compat.sh proto'", "phase migsafety env CI_BASE=\"$base\" CI_QUEUE_ENTRY=\"$queue\" bash -c", "scripts/check-compat.sh migrations", "phase vet", "gate-report", "summary.md", `-mem-peak "$cg_peak" -mem-limit "$mem_limit"`, `>"$L/$n.oom"`} {
		if !strings.Contains(merge, s) {
			t.Errorf("merge missing %s", s)
		}
	}
	for _, s := range []string{"task generate:docs:check", "task build:web:verify", "task test -- $pkgs", "task test:web", "task test:electron", "task test:integration", "cmd_assemble", "PARTS=(static lint go web)", "task test:e2e", "go build ./...", "go vet ./...", "STEPS=(npm generate docs lint build webbuild", "missing_steps"} {
		if !strings.Contains(release, s) {
			t.Errorf("release missing %s", s)
		}
	}
	pl := &Plan{Go: GoPlan{Packages: []string{"./internal/x"}}}
	dir := gateDir(t, pl, map[string]int{"build": 0, "test": 0})
	problems := strings.Join(verifyGate(filepath.Join(dir, "plan.json"), dir), ";")
	if !strings.Contains(problems, "vet") {
		t.Fatalf("missing obliged checks: %s", problems)
	}
}

// A red typecheck printed only "typecheck | FAIL (2)": tsc's diagnostics match
// no test-failure pattern and the log stayed on the runner (bug report
// 7e9be182). They now lead the report; a failed phase whose log names nothing
// gets its last lines instead.
func TestGateReportNamesTypecheckErrorsAndTailsAnUnnamedFailure(t *testing.T) {
	dir := t.TempDir()
	tsc := strings.Repeat("noise\n", 50) + "src/a.ts(12,3): error TS2322: Type 'string' is not assignable to type 'number'.\n"
	os.WriteFile(filepath.Join(dir, "typecheck.log"), []byte(tsc), 0600)
	os.WriteFile(filepath.Join(dir, "typecheck.row"), []byte("typecheck 2 10 0\n"), 0600)
	opaque := strings.Repeat("setup line\n", 60) + "\x1b[31mnpm ERR! missing script: lint\x1b[0m\n"
	os.WriteFile(filepath.Join(dir, "lint.log"), []byte(opaque), 0600)
	os.WriteFile(filepath.Join(dir, "lint.row"), []byte("lint 1 5 0\n"), 0600)
	var out bytes.Buffer
	if rc := runGateReport([]string{"-dir", dir, "-wall", "10", "-budgets", filepath.Join(repoRoot, "scripts/merge-gate-budgets.json")}, &out, &out); rc != 0 {
		t.Fatalf("rc %d: %s", rc, &out)
	}
	s := out.String()
	if !strings.HasPrefix(s, "src/a.ts(12,3): error TS2322: Type 'string' is not assignable to type 'number'.\n") {
		t.Fatalf("tsc error must lead the report: %s", s)
	}
	if strings.Contains(s, "noise") {
		t.Fatalf("a phase whose failure is named must not be tailed: %s", s)
	}
	if !strings.Contains(s, "last lines of lint.log") || !strings.Contains(s, "    npm ERR! missing script: lint\n") {
		t.Fatalf("an unnamed failure must show its log's tail, ANSI-stripped: %s", s)
	}
	if n := strings.Count(s, "    setup line\n"); n != failedLogTail-1 {
		t.Fatalf("tail must be %d lines, got %d setup lines: %s", failedLogTail, n, s)
	}
}

// reliant#2778's merge group failed its test phase at a 14.6 GiB peak of a
// 15 GiB runner with a log naming no failure but "signal: killed": the OOM
// killer, not the PR. The marker control-plane's pr-fix re-runs on
// (control-plane #2862) is printed only when every failed phase is such a
// kill, on evidence attributable to that phase (its own SIGKILL).
func TestOOMMarkersOnlyForAKilledPhaseAtTheMemoryLimit(t *testing.T) {
	const gib = int64(1) << 30
	limit := 15 * gib
	near := limit * 95 / 100
	cases := []struct {
		name    string
		named   bool
		failed  []string
		unnamed []string
		killed  []string
		peak    int64
		limit   int64
		kills   map[string]int64
		want    []string
	}{
		{name: "killed at the limit", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, peak: near, limit: limit,
			want: []string{"!! GATE-INFRA: oom test (memory peak 14.2 GiB of 15.0 GiB; killed, and its log names no failure): the runner ran out of memory, NOT PR blame."}},
		{name: "killed and oom_kill rose", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, kills: map[string]int64{"test": 2},
			want: []string{"!! GATE-INFRA: oom test (2 oom_kill while it ran; killed, and its log names no failure): the runner ran out of memory, NOT PR blame."}},
		{name: "both", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, peak: limit, limit: limit, kills: map[string]int64{"test": 1},
			want: []string{"!! GATE-INFRA: oom test (memory peak 15.0 GiB of 15.0 GiB, 1 oom_kill while it ran; killed, and its log names no failure): the runner ran out of memory, NOT PR blame."}},
		// A memory-heavy phase that succeeded, then a build failing with an
		// ordinary diagnostic: the gate-wide peak and kill count say nothing
		// about the build, which was not killed.
		{name: "a high peak before an ordinary failure", failed: []string{"build"}, unnamed: []string{"build"}, peak: limit, limit: limit},
		{name: "a shared oom_kill rise on an ordinary failure", failed: []string{"build"}, unnamed: []string{"build"}, kills: map[string]int64{"build": 1}},
		// One concurrent phase killed, another failing normally in the same
		// window: the counter rose for both, but only one was killed.
		{name: "one concurrent phase killed, another failing normally", failed: []string{"lint", "test"}, unnamed: []string{"lint", "test"}, killed: []string{"test"},
			peak: limit, limit: limit, kills: map[string]int64{"lint": 1, "test": 1}},
		{name: "killed with no memory evidence", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}},
		{name: "peak just under the threshold", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, peak: near - 1, limit: limit},
		{name: "unknown limit", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, peak: limit},
		{name: "unknown peak", failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, limit: limit},
		{name: "the log names a failure", failed: []string{"test"}, killed: []string{"test"}, peak: limit, limit: limit, kills: map[string]int64{"test": 1}},
		{name: "a named failure elsewhere", named: true, failed: []string{"test"}, unnamed: []string{"test"}, killed: []string{"test"}, peak: limit, limit: limit},
		{name: "two killed phases", failed: []string{"build", "test"}, unnamed: []string{"build", "test"}, killed: []string{"build", "test"}, kills: map[string]int64{"build": 1, "test": 1},
			want: []string{
				"!! GATE-INFRA: oom build (1 oom_kill while it ran; killed, and its log names no failure): the runner ran out of memory, NOT PR blame.",
				"!! GATE-INFRA: oom test (1 oom_kill while it ran; killed, and its log names no failure): the runner ran out of memory, NOT PR blame.",
			}},
		{name: "nothing failed", peak: limit, limit: limit},
	}
	set := func(names []string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := oomMarkers(c.named, c.failed, set(c.unnamed), set(c.killed), c.peak, c.limit, c.kills)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestEndedByKill(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		log  string
		rc   int
		want bool
	}{
		{"ok  \texample/p\t1.2s\nsignal: killed\n", 1, true},
		{"FAIL\texample/p [build failed]\n", 1, false},
		{"./scripts/x.sh: line 4: 812 Killed                  go test ./...\n", 1, true},
		{"internal/x/y.go:3:1: syntax error: unexpected }\n", 1, false},
		{"", 137, true},
		{"the build was not killed\n", 2, false},
	} {
		p := filepath.Join(dir, "p.log")
		os.WriteFile(p, []byte(c.log), 0600)
		if got := endedByKill(p, c.rc); got != c.want {
			t.Errorf("endedByKill(%q, %d) = %v, want %v", c.log, c.rc, got, c.want)
		}
	}
}

// End to end through gate-report: the .oom file phase() writes and the memory
// flags merge-gate.sh passes reach the marker; without them nothing changes.
func TestGateReportMarksAnOOMKilledPhase(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.log"), []byte(strings.Repeat("ok  \texample/p\t1.2s\n", 5)+"signal: killed\n"), 0600)
	os.WriteFile(filepath.Join(dir, "test.row"), []byte("test 1 805 30\n"), 0600)
	os.WriteFile(filepath.Join(dir, "build.row"), []byte("build 0 40 0\n"), 0600)
	budgets := filepath.Join(repoRoot, "scripts/merge-gate-budgets.json")
	report := func(args ...string) string {
		var out bytes.Buffer
		if rc := runGateReport(append([]string{"-dir", dir, "-wall", "10", "-budgets", budgets}, args...), &out, &out); rc != 0 {
			t.Fatalf("rc %d: %s", rc, &out)
		}
		return out.String()
	}
	if s := report(); strings.Contains(s, "GATE-INFRA") || !strings.Contains(s, "last lines of test.log") {
		t.Fatalf("no memory evidence must print no marker: %s", s)
	}
	if s := report("-mem-peak", "15676289024", "-mem-limit", "16106127360"); !strings.Contains(s, "!! GATE-INFRA: oom test (memory peak 14.6 GiB of 15.0 GiB;") {
		t.Fatalf("a peak at 97%% of the limit must mark the phase: %s", s)
	}
	os.WriteFile(filepath.Join(dir, "test.oom"), []byte("1\n"), 0600)
	if s := report(); !strings.Contains(s, "!! GATE-INFRA: oom test (1 oom_kill while it ran;") {
		t.Fatalf("an oom_kill during the phase must mark it: %s", s)
	}
	// The same peak and kill count beside a build that failed on its own
	// (no SIGKILL in its log) mark nothing: the kill was another phase's.
	os.WriteFile(filepath.Join(dir, "build.log"), []byte("go: building\nexit status 1\n"), 0600)
	os.WriteFile(filepath.Join(dir, "build.row"), []byte("build 1 40 0\n"), 0600)
	os.WriteFile(filepath.Join(dir, "build.oom"), []byte("1\n"), 0600)
	if s := report("-mem-peak", "15676289024", "-mem-limit", "16106127360"); strings.Contains(s, "GATE-INFRA") {
		t.Fatalf("an ordinary failure beside a killed phase must print no marker: %s", s)
	}
}

// A phase SIGKILLed before it printed anything (rc 137, an empty but
// readable log) is the commonest OOM shape and must still be marked; an
// unreadable log stays fail-closed.
func TestGateReportMarksASilentlyKilledPhase(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build.log"), nil, 0600)
	os.WriteFile(filepath.Join(dir, "build.row"), []byte("build 137 40 0\n"), 0600)
	os.WriteFile(filepath.Join(dir, "build.oom"), []byte("1\n"), 0600)
	budgets := filepath.Join(repoRoot, "scripts/merge-gate-budgets.json")
	report := func() string {
		var out bytes.Buffer
		if rc := runGateReport([]string{"-dir", dir, "-wall", "10", "-budgets", budgets}, &out, &out); rc != 0 {
			t.Fatalf("rc %d: %s", rc, &out)
		}
		return out.String()
	}
	if s := report(); !strings.Contains(s, "!! GATE-INFRA: oom build (1 oom_kill while it ran;") || !strings.Contains(s, "build.log is empty") {
		t.Fatalf("an empty log, rc 137 and an oom_kill must mark the phase: %s", s)
	}
	os.Remove(filepath.Join(dir, "build.log"))
	if s := report(); strings.Contains(s, "GATE-INFRA") {
		t.Fatalf("an unreadable log must print no marker: %s", s)
	}
}
