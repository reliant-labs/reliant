package scaffolds

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLintWorkaroundsRoot_Clean(t *testing.T) {
	t.Parallel()
	res, err := LintWorkaroundsRoot(filepath.Join("testdata", "check_workarounds", "clean"))
	if err != nil {
		t.Fatalf("LintWorkaroundsRoot returned error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected zero findings on clean fixture, got %d: %+v", len(res.Findings), res.Findings)
	}
	// Workaround findings are warnings only; the clean fixture must
	// also not trip HasErrors().
	if res.HasErrors() {
		t.Fatal("clean fixture must not produce errors")
	}
}

func TestLintWorkaroundsRoot_Firing(t *testing.T) {
	t.Parallel()
	res, err := LintWorkaroundsRoot(filepath.Join("testdata", "check_workarounds", "firing"))
	if err != nil {
		t.Fatalf("LintWorkaroundsRoot returned error: %v", err)
	}
	wantRules := map[string]bool{
		"workaround-testing-extras":      false,
		"workaround-cmd-not-in-binaries": false,
	}
	for _, f := range res.Findings {
		if _, ok := wantRules[f.Rule]; ok {
			wantRules[f.Rule] = true
		}
		// All workaround findings must be warnings.
		if f.Severity != SeverityWarning {
			t.Errorf("rule %s: expected severity %q, got %q", f.Rule, SeverityWarning, f.Severity)
		}
	}
	for rule, fired := range wantRules {
		if !fired {
			t.Errorf("expected rule %s to fire on firing fixture, got findings: %+v", rule, res.Findings)
		}
	}
	// Workaround findings never gate the build.
	if res.HasErrors() {
		t.Fatal("workaround findings must be warnings, not errors")
	}
}

// TestLintWorkaroundsRoot_TestingExtrasAdviceIsCurrent pins the advice, not
// just the rule id. The message shipped for a month saying to "remove once
// `bootstrap_testing.go.tmpl` auto-stubs interface-typed Deps" — future
// tense — after computeAutoStubs had already landed that exact capability
// (1b543069, 2026-06-03, extending it to cross-package selector types).
//
// Stale advice in a linter is not a cosmetic defect: it reads as an OPEN GAP,
// and an open gap is the standing justification for keeping an obsolete fork
// alive. This one was cited as live evidence in an ownership audit, and
// control-plane disowned pkg/app/testing.go on 2026-07-02 — a month after the
// fix — on the strength of it. The rule id firing tells us nothing about
// whether the sentence is still true, so assert the sentence.
func TestLintWorkaroundsRoot_TestingExtrasAdviceIsCurrent(t *testing.T) {
	t.Parallel()
	res, err := LintWorkaroundsRoot(filepath.Join("testdata", "check_workarounds", "firing"))
	if err != nil {
		t.Fatalf("LintWorkaroundsRoot returned error: %v", err)
	}
	var msg string
	for _, f := range res.Findings {
		if f.Rule == "workaround-testing-extras" {
			msg = f.Message
		}
	}
	if msg == "" {
		t.Fatalf("workaround-testing-extras did not fire on the firing fixture: %+v", res.Findings)
	}
	// The advice must point at the mechanism that EXISTS.
	for _, want := range []string{
		"CLOSED",
		"computeDepsDoubles",
		"With<Svc>Deps",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must name the shipped mechanism %q\ngot: %s", want, msg)
		}
	}
	// And must not describe the closed gap as future work. "once <X> auto-stubs"
	// is the exact phrasing that rotted.
	for _, bad := range []string{
		"remove once",
		"auto-stubs interface-typed Deps;",
	} {
		if strings.Contains(msg, bad) {
			t.Errorf("message still advertises the closed gap as future work (%q)\ngot: %s", bad, msg)
		}
	}
}

func TestReadDeclaredBinaries(t *testing.T) {
	t.Parallel()
	got := readDeclaredBinaries(filepath.Join("testdata", "check_workarounds", "clean", "forge.yaml"))
	if !got["server"] {
		t.Errorf("expected server in declared binaries, got %+v", got)
	}
	if !got["workspace-proxy"] {
		t.Errorf("expected workspace-proxy in declared binaries, got %+v", got)
	}
}

func TestIsExemptCmdFile(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"server":          true,
		"root":            true,
		"version":         true,
		"main":            true,
		"db":              true,
		"otel":            true,
		"_shared":         true,
		"foo_shared":      true,
		"workspace_proxy": false,
		"extra":           false,
	}
	for in, want := range cases {
		if got := isExemptCmdFile(in); got != want {
			t.Errorf("isExemptCmdFile(%q) = %v, want %v", in, got, want)
		}
	}
}
