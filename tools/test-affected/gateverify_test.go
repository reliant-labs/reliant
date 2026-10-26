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
	var out, errOut bytes.Buffer
	code := runGateVerify([]string{"-plan", filepath.Join(dir, "plan.json"), "-dir", dir}, &out, &errOut)
	return code, out.String() + errOut.String()
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
	for _, want := range []string{"migsafety", "lint"} {
		if !strings.Contains(v, want) {
			t.Fatalf("verdict %q does not name %s", v, want)
		}
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
