package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario-index emitters (WriteScenariosIndex / EmitScenarioScaffolding /
// scenarioImportIdent, all in frontend_mocks.go) are exercised here.
// TestValidateScenarioName lives with `forge scaffold scenario` in the
// internal/cli/scaffold group (scaffold/scenario_test.go).

func TestScenarioImportIdent(t *testing.T) {
	cases := map[string]string{
		"default":            "scenario_default",
		"github-connected":   "scenario_githubConnected",
		"a-b-c":              "scenario_aBC",
		"single":             "scenario_single",
		"github-1-connected": "scenario_github1Connected",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got := scenarioImportIdent(in)
			if got != want {
				t.Errorf("scenarioImportIdent(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestWriteScenariosIndex_Idempotent verifies that re-running
// WriteScenariosIndex on the same directory contents produces
// byte-identical output.
func TestWriteScenariosIndex_Idempotent(t *testing.T) {
	dir := t.TempDir()

	// Seed two scenario files.
	mustWriteScenarioFile(t, filepath.Join(dir, "default.ts"), "// default")
	mustWriteScenarioFile(t, filepath.Join(dir, "github-connected.ts"), "// gh")

	if err := WriteScenariosIndex(dir, ".", nil); err != nil {
		t.Fatalf("first WriteScenariosIndex: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "index_gen.ts"))
	if err != nil {
		t.Fatalf("read first index: %v", err)
	}

	if err := WriteScenariosIndex(dir, ".", nil); err != nil {
		t.Fatalf("second WriteScenariosIndex: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "index_gen.ts"))
	if err != nil {
		t.Fatalf("read second index: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("non-idempotent regeneration:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Sanity-check expected content.
	body := string(first)
	for _, expect := range []string{
		`import scenario_default from "./default";`,
		`import scenario_githubConnected from "./github-connected";`,
		`[scenario_default.name]: scenario_default,`,
		`[scenario_githubConnected.name]: scenario_githubConnected,`,
		`export { default as defaultScenario } from "./default";`,
	} {
		if !strings.Contains(body, expect) {
			t.Errorf("index missing %q in:\n%s", expect, body)
		}
	}
}

// TestEmitScenarioScaffolding_SeedsDefaultOnce verifies that emitting
// scaffolding twice doesn't overwrite a hand-edited default.ts.
func TestEmitScenarioScaffolding_SeedsDefaultOnce(t *testing.T) {
	dir := t.TempDir()
	mocksRel := filepath.Join("src", "mocks")
	mocksDir := filepath.Join(dir, mocksRel)
	if err := os.MkdirAll(mocksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cs, err := LoadChecksums(dir)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}
	if err := EmitScenarioScaffolding(dir, mocksRel, nil, cs); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	defaultPath := filepath.Join(mocksDir, "scenarios", "default.ts")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Fatalf("default.ts not written: %v", err)
	}

	// Hand-edit default.ts.
	mustWriteScenarioFile(t, defaultPath, "// HAND-EDITED")

	if err := EmitScenarioScaffolding(dir, mocksRel, nil, cs); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	got, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read default.ts: %v", err)
	}
	if string(got) != "// HAND-EDITED" {
		t.Errorf("default.ts was overwritten on re-emit; got:\n%s", got)
	}

	// scenario-types_gen.ts is regenerated every run — so it should exist and
	// not be the hand-edited marker.
	typesPath := filepath.Join(mocksDir, "scenario-types_gen.ts")
	tb, err := os.ReadFile(typesPath)
	if err != nil {
		t.Fatalf("read scenario-types_gen.ts: %v", err)
	}
	if !strings.Contains(string(tb), "export interface Scenario") {
		t.Errorf("scenario-types_gen.ts missing Scenario interface; got:\n%s", tb)
	}
	// Hybrid mode contract: the passthrough opt-in must be exported on the
	// Scenario interface, otherwise scenarios can't forward unmatched RPCs
	// to the real backend and the hybrid wiring is dead code.
	if !strings.Contains(string(tb), "passthrough?:") {
		t.Errorf("scenario-types_gen.ts missing %q (required for hybrid mode); got:\n%s", "passthrough?:", tb)
	}

	// index_gen.ts exists and references the hand-edited default.
	indexPath := filepath.Join(mocksDir, "scenarios", "index_gen.ts")
	ib, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index_gen.ts: %v", err)
	}
	if !strings.Contains(string(ib), `from "./default"`) {
		t.Errorf("index_gen.ts missing default reference; got:\n%s", ib)
	}
}

func mustWriteScenarioFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
