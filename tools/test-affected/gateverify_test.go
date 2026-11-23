// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gateDir builds a merge-gate log directory: plan.json for pl and one
// "<name> <rc> <secs> <start>" row per phase in rows.
func gateDir(t *testing.T, pl *Plan, rows map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(pl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, rc := range rows {
		if err := os.WriteFile(filepath.Join(dir, name+".row"), []byte(fmt.Sprintf("%s %d 3 1\n", name, rc)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func verify(t *testing.T, dir string) (int, string) {
	t.Helper()
	// These fixtures test obligation accounting independently of the CLI's
	// checkout identity boundary (covered by plan_identity_test.go).
	problems := verifyGate(filepath.Join(dir, "plan.json"), dir)
	text := "OK"
	code := 0
	if len(problems) > 0 {
		code = 1
		text = "FAILED: " + strings.Join(problems, "; ")
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return code, text
}

func verdict(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "verdict"))
	if err != nil {
		t.Fatalf("no verdict file: %v", err)
	}
	return strings.TrimSpace(string(b))
}

func selected(reason string) LaneFull { return LaneFull{Full: true, Reasons: []string{reason}} }

// The #1393 shape: the docs-generate lane was selected, sat queued for a slot,
// and never ran, while build and test were green. That must not pass.
func TestSelectedLaneThatNeverRanFailsAndIsNamed(t *testing.T) {
	pl := &Plan{DocGen: DocGenPlan{LaneFull: selected("internal/x: docs input")}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0})
	code, out := verify(t, dir)
	if code == 0 {
		t.Fatalf("a selected docs lane that never ran passed:\n%s", out)
	}
	if v := verdict(t, dir); !strings.Contains(v, "docs") || !strings.HasPrefix(v, "FAILED") {
		t.Fatalf("verdict %q does not name the docs lane", v)
	}
	if !strings.Contains(out, "never completed") {
		t.Fatalf("output does not say the lane never completed:\n%s", out)
	}
}

func TestEverySelectedLaneCompletedPasses(t *testing.T) {
	pl := &Plan{
		ForgeGen: ForgeGenPlan{LaneFull: selected("forge input")},
		DocGen:   DocGenPlan{LaneFull: selected("docs input")},
		MigOrder: MigOrderPlan{LaneFull: selected("db/migrations/1_a.up.sql")},
	}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0, "generate": 0, "docs": 0, "migorder": 0})
	if code, out := verify(t, dir); code != 0 {
		t.Fatalf("all selected lanes completed, got exit %d:\n%s", code, out)
	}
	if v := verdict(t, dir); v != "OK" {
		t.Fatalf("verdict %q, want OK", v)
	}
}

func TestSelectedLaneThatFailedIsNamed(t *testing.T) {
	pl := &Plan{ForgeGen: ForgeGenPlan{LaneFull: selected("forge input")}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0, "generate": 1})
	code, _ := verify(t, dir)
	if code == 0 {
		t.Fatal("a failed generate lane passed")
	}
	if v := verdict(t, dir); !strings.Contains(v, "generate") {
		t.Fatalf("verdict %q does not name generate", v)
	}
}

// build and test are not plan-selected: every gate runs them, so a gate whose
// test phase left no row (killed, cancelled, tool missing) must fail.
func TestTestPhaseWithoutARowFails(t *testing.T) {
	dir := gateDir(t, &Plan{}, map[string]int{"plan": 0, "build": 0})
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a gate whose test phase never completed passed")
	}
	if v := verdict(t, dir); !strings.Contains(v, "test") {
		t.Fatalf("verdict %q does not name test", v)
	}
}

func TestNothingSelectedBeyondBuildAndTestPasses(t *testing.T) {
	dir := gateDir(t, &Plan{}, map[string]int{"plan": 0, "build": 0, "test": 0})
	if code, out := verify(t, dir); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The web type-check is chosen by the script from the changed files; the
// verifier derives the same obligation from the plan's file list.
func TestWebChangeRequiresTypecheck(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "frontends/web/src/a.ts"}}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0})
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a web change passed without a typecheck row")
	}
	if v := verdict(t, dir); !strings.Contains(v, "typecheck") {
		t.Fatalf("verdict %q does not name typecheck", v)
	}
}

func TestMigrationChangeRequiresMigsafetyAndLintOnAPinMove(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "db/migrations/1_a.up.sql"}, {Path: "go.mod"}}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0})
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a migration and a pin bump passed without migsafety or lint")
	}
	v := verdict(t, dir)
	for _, want := range []string{"migsafety", "closedsets", "lint"} {
		if !strings.Contains(v, want) {
			t.Fatalf("verdict %q does not name %s", v, want)
		}
	}
}

// forge lint --closed-sets is obliged whenever a migration or the forge pin
// (go.mod, forge.yaml) moves, and not otherwise: a pin bump that grew
// accesstoken.AllScopes merged without the widening migration and turned the
// release gate red (#3382).
func TestClosedSetsRunOnAMigrationOrAPinMove(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"a migration", []string{"db/migrations/1_a.up.sql"}, true},
		{"go.mod", []string{"go.mod"}, true},
		{"forge.yaml", []string{"forge.yaml"}, true},
		{"go.sum alone", []string{"go.sum"}, false},
		{"unrelated code", []string{"internal/x/x.go", "README.md"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var files []FileReport
			for _, p := range tc.files {
				files = append(files, FileReport{Path: p})
			}
			dir := gateDir(t, &Plan{Files: files}, map[string]int{"plan": 0, "build": 0, "test": 0, "migsafety": 0, "migorder": 0, "lint": 0, "golangci": 0, "vet": 0})
			code, _ := verify(t, dir)
			if tc.want {
				if code == 0 || !strings.Contains(verdict(t, dir), "closedsets never completed") {
					t.Fatalf("%v passed without a closedsets row: %q", tc.files, verdict(t, dir))
				}
				if err := os.WriteFile(filepath.Join(dir, "closedsets.row"), []byte("closedsets 0 1 0\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if code, out := verify(t, dir); code != 0 {
					t.Fatalf("%v with a closedsets row failed: %s", tc.files, out)
				}
				return
			}
			if code, out := verify(t, dir); code != 0 {
				t.Fatalf("%v needs no closedsets row: %s", tc.files, out)
			}
		})
	}
}

// A closedsets phase that FAILED fails the gate, naming it.
func TestFailedClosedSetsFailTheGate(t *testing.T) {
	dir := gateDir(t, &Plan{Files: []FileReport{{Path: "go.mod"}}}, map[string]int{"plan": 0, "build": 0, "test": 0, "migsafety": 0, "closedsets": 1})
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a failing closedsets row passed")
	}
	if v := verdict(t, dir); !strings.Contains(v, "closedsets") {
		t.Fatalf("verdict %q does not name closedsets", v)
	}
}

// A diff that moves only root pins has no directory to scope lint to: the
// whole-module lint a pin needs runs in the release gate, so the merge gate is
// not obliged to lint (migsafety still follows forge.yaml).
func TestPinOnlyChangeDoesNotRequireLint(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "go.mod"}, {Path: "go.sum"}, {Path: "forge.yaml"}}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0, "migsafety": 0, "closedsets": 0})
	if code, out := verify(t, dir); code != 0 {
		t.Fatalf("a pin-only change must pass without a lint row: %s", out)
	}
}

// --skip-lint leaves vet and the scoped forge lint to the release gate. The
// merge queue's tier (merge-gate.sh --queue) leaves vet to it but runs the
// scoped forge lint: with none, #2696 and #2662 reached main on findings it
// names. Everything else both are obliged to keep. golangci (the queue's
// golangci (changed) job, run by the gate) is obliged on a .go change unless
// the gate marked golangci-elsewhere (--queue, --skip-golangci): such a job
// runs beside it.
func TestQueueTierRequiresLintButNotVet(t *testing.T) {
	pl := &Plan{
		Files: []FileReport{{Path: "internal/x/x.go"}, {Path: "db/migrations/1_a.up.sql"}},
		Go:    GoPlan{Build: []string{"./internal/x"}, Packages: []string{"./internal/x"}},
	}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0})
	for _, tc := range []struct {
		tier       string
		elsewhere  bool // the gate wrote golangci-elsewhere
		obliged    []string
		notObliged []string
	}{
		{"queue", true, []string{"migsafety", "closedsets", "lint"}, []string{"vet", "golangci"}},
		{"skip-lint", true, []string{"migsafety", "closedsets"}, []string{"vet", "lint", "golangci"}},
		{"skip-lint", false, []string{"migsafety", "closedsets", "golangci"}, []string{"vet", "lint"}},
		{"", true, []string{"migsafety", "closedsets", "lint", "vet"}, []string{"golangci"}},
		{"", false, []string{"migsafety", "closedsets", "lint", "vet", "golangci"}, nil},
	} {
		_ = os.Remove(filepath.Join(dir, "tier"))
		_ = os.Remove(filepath.Join(dir, "golangci-elsewhere"))
		if tc.elsewhere {
			if err := os.WriteFile(filepath.Join(dir, "golangci-elsewhere"), []byte("a job beside the gate\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if tc.tier != "" {
			if err := os.WriteFile(filepath.Join(dir, "tier"), []byte(tc.tier+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if code, _ := verify(t, dir); code == 0 {
			t.Fatalf("the %q tier passed a migration without migsafety", tc.tier)
		}
		v := verdict(t, dir)
		for _, want := range tc.obliged {
			if !strings.Contains(v, want+" never completed") {
				t.Errorf("the %q tier did not oblige %s: %q", tc.tier, want, v)
			}
		}
		for _, unwanted := range tc.notObliged {
			if strings.Contains(v, unwanted+" never completed") {
				t.Errorf("the %q tier obliged %s: %q", tc.tier, unwanted, v)
			}
		}
	}
}

// golangci's verdict is the gate's: a finding (golangci-lint's exit 1, a G101
// hard-coded credential say) fails it by name, a green row passes, and a diff
// that only deletes Go files obliges no lint.
func TestGolangciRowDecidesTheGate(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "internal/x/x.go"}}}
	base := map[string]int{"plan": 0, "build": 0, "test": 0, "lint": 0, "vet": 0}
	rows := func(golangci int) map[string]int {
		r := map[string]int{"golangci": golangci}
		for k, v := range base {
			r[k] = v
		}
		return r
	}
	if code, v := verify(t, gateDir(t, pl, rows(1))); code == 0 || !strings.Contains(v, "golangci failed (rc 1)") {
		t.Errorf("a golangci finding passed the gate: %q", v)
	}
	if code, v := verify(t, gateDir(t, pl, rows(0))); code != 0 {
		t.Errorf("a clean golangci failed the gate: %q", v)
	}
	gone := &Plan{Files: []FileReport{{Path: "internal/x/x.go", Deleted: true}, {Path: "internal/x/README.md"}}}
	if code, v := verify(t, gateDir(t, gone, base)); code != 0 {
		t.Errorf("a diff deleting its only Go file obliged golangci: %q", v)
	}
}

// A selected demo build that never ran fails the gate, by name.
func TestSelectedDemoBuildIsObliged(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "frontends/web/src/hooks/useOrgs.ts"}}, WebDemo: WebDemoPlan{LaneFull: selected("in the forge demo bundle's import graph")}}
	dir := gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0, "typecheck": 0, "lint": 0})
	if code, _ := verify(t, dir); code == 0 || !strings.Contains(verdict(t, dir), "demo never completed") {
		t.Fatalf("a selected demo build with no row passed: %q", verdict(t, dir))
	}
	dir = gateDir(t, pl, map[string]int{"plan": 0, "build": 0, "test": 0, "typecheck": 0, "lint": 0, "demo": 0})
	if code, v := verify(t, dir); code != 0 {
		t.Fatalf("a completed demo build failed: %q", v)
	}
}

// A phase the script decided to run is declared in <dir>/expected; one that
// left no row (the gate was cut off before it finished) fails.
func TestDeclaredPhaseWithoutARowFails(t *testing.T) {
	dir := gateDir(t, &Plan{}, map[string]int{"plan": 0, "build": 0, "test": 0})
	if err := os.WriteFile(filepath.Join(dir, "expected"), []byte("lint\nbuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a declared lint phase with no row passed")
	}
	if v := verdict(t, dir); !strings.Contains(v, "lint") {
		t.Fatalf("verdict %q does not name lint", v)
	}
}

// A plan the verifier cannot read is not a plan with nothing selected.
func TestUnreadablePlanFails(t *testing.T) {
	dir := gateDir(t, &Plan{}, map[string]int{"plan": 0, "build": 0, "test": 0})
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("an unreadable plan passed")
	}
	if v := verdict(t, dir); !strings.HasPrefix(v, "FAILED") {
		t.Fatalf("verdict %q", v)
	}
}

func TestMissingPlanFails(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if code := runGateVerify([]string{"-plan", filepath.Join(dir, "plan.json"), "-dir", dir}, &out, &out); code == 0 {
		t.Fatal("a missing plan passed")
	}
}

// A row that is garbled counts as no row.
func TestMalformedRowIsNotACompletion(t *testing.T) {
	dir := gateDir(t, &Plan{}, map[string]int{"plan": 0, "build": 0})
	if err := os.WriteFile(filepath.Join(dir, "test.row"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := verify(t, dir); code == 0 {
		t.Fatal("a malformed test row passed")
	}
}

// merge-gate.sh must call the verifier and may not read the plan unguarded.
func TestMergeGateScriptWiresTheVerifier(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"gate-verify", "command -v jq", `"$L/expected"`} {
		if !strings.Contains(src, want) {
			t.Errorf("scripts/merge-gate.sh must contain %q (a selected lane that never completed must fail the gate)", want)
		}
	}
}
