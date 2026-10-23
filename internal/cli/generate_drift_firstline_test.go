// Tests for the stomp-guard error's FIRST LINE and single-print
// contract.
//
// Journey fr-a04f8c0609: the Tier-1 stomp guard fired with the visible
// message 'Tier-1 file-stomp guard:' — no file, no remedy — printed
// twice. Root causes:
//
//  1. Everything actionable (file list, escape hatches) lived BELOW the
//     first newline of the error. Agent harnesses, log pipelines, and
//     wrap-and-rethrow callers routinely surface only an error's first
//     line, so the user saw a bare header. The same guard produced a
//     proper 13-file listing in a terminal run — the body was always
//     there, just never on the line that survives truncation.
//  2. The error was printed twice: cobra's default error handling
//     printed "Error: <err>" + the full usage dump, then main.go
//     printed "Error: <err>" again.
//
// These tests pin the fixes: the first line of the guard error names
// the drifted files and the remedies, and the root command silences
// cobra's duplicate print/usage spam so main.go's single print is the
// only one.
package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// driftFirstLine extracts the first line of an error message.
func driftFirstLine(err error) string {
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

// TestStepCheckTier1Drift_FirstLineNamesFilesAndRemedies drives the
// real guard step over a project with two drifted Tier-1 files and
// requires the error's FIRST line to stand alone: every drifted path
// and the --force / `forge project disown` remedies must appear before the
// first newline.
func TestStepCheckTier1Drift_FirstLineNamesFilesAndRemedies(t *testing.T) {
	dir := t.TempDir()
	mustWriteScopeFile(t, filepath.Join(dir, "proto", "services", "api", "v1", "api.proto"), "syntax = \"proto3\";\n")

	cs := &checksums.FileChecksums{}
	drifted := []string{"pkg/app/wire_gen.go", "internal/cli/serve.go"}
	for _, rel := range drifted {
		// Hand-edited Tier-1 file: marker carries the as-generated hash,
		// body is the edit → Verify == Modified.
		stamped, ok := checksums.StampWithValue(rel,
			[]byte("package x // hand-edited\n"),
			checksums.BodyHash([]byte("package x // as generated\n")))
		if !ok {
			t.Fatalf("stamp %s: unstampable", rel)
		}
		mustWriteScopeFile(t, filepath.Join(dir, rel), string(stamped))
	}

	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: cs}
	err := checkTier1DriftAndAdjudicate(t, ctx)
	if err == nil {
		t.Fatal("stepCheckTier1Drift = nil; want the Tier-1 stomp-guard error")
	}

	first := driftFirstLine(err)
	for _, want := range append(drifted, "--force", "forge project disown") {
		if !strings.Contains(first, want) {
			t.Errorf("guard error first line missing %q.\nfirst line: %s\nfull error:\n%v", want, first, err)
		}
	}
	// The body (full report with extension-point hints) must still follow.
	if !strings.Contains(err.Error(), "extension point") {
		t.Errorf("guard error lost the full report body:\n%v", err)
	}
}

// TestTier1DriftSummaryLine_TruncatesLongLists pins the inline-list cap:
// a 12-file drift set names the first 8 and counts the rest, so the
// first line stays a line.
func TestTier1DriftSummaryLine_TruncatesLongLists(t *testing.T) {
	var drift []checksums.Tier1DriftEntry
	for i := 0; i < 12; i++ {
		drift = append(drift, checksums.Tier1DriftEntry{Path: fmt.Sprintf("pkg/file%02d.go", i)})
	}
	got := tier1DriftSummaryLine(drift)
	if strings.Contains(got, "\n") {
		t.Errorf("summary line contains a newline:\n%s", got)
	}
	if !strings.Contains(got, "12 hand-edited Tier-1 file(s)") {
		t.Errorf("summary should count all 12 files; got: %s", got)
	}
	if !strings.Contains(got, "pkg/file07.go") {
		t.Errorf("summary should name the first 8 files; got: %s", got)
	}
	if strings.Contains(got, "pkg/file08.go") {
		t.Errorf("summary should truncate after 8 files; got: %s", got)
	}
	if !strings.Contains(got, "+4 more") {
		t.Errorf("summary should count the truncated remainder; got: %s", got)
	}
}

// TestRootCmdPrintsErrorsOnce pins the single-print contract: cobra's
// own error/usage printing is silenced at the root (and inherited by
// every subcommand), leaving cmd/forge/main.go's "Error: %v" as the
// one and only print. Without this, every pipeline failure — including
// the stomp-guard report — was printed twice with a full usage dump
// sandwiched between the copies.
//
// Usage suppression is scoped to RUNTIME errors only: SilenceUsage is
// not set statically but in PersistentPreRun (after flag/arg parsing
// succeeds), so genuine usage mistakes still print the help block. The
// runtime-vs-usage behavioral split is pinned by
// TestRootCmd_RuntimeErrorDoesNotDumpUsage and friends in root_test.go;
// here we pin that PersistentPreRun installs the suppression every
// subcommand inherits.
func TestRootCmdPrintsErrorsOnce(t *testing.T) {
	root := newTestRootCmd(t)
	if !root.SilenceErrors {
		t.Error("root command must set SilenceErrors: main.go already prints the returned error; cobra printing it too duplicates every failure report")
	}
	if root.SilenceUsage {
		t.Error("root command must NOT set SilenceUsage statically: usage mistakes (unknown flag, wrong arg count) should keep the help block; runtime suppression belongs in PersistentPreRun")
	}
	// PersistentPreRunE, not PersistentPreRun: the hook also installs the
	// --project-dir resolution root, which can fail (a -C that is missing or
	// is a file), and that must surface as a command error rather than a
	// panic or a silent fallback to the CWD.
	if root.PersistentPreRunE == nil {
		t.Fatal("root command must install PersistentPreRunE: it scopes SilenceUsage to runtime errors")
	}
	if err := root.PersistentPreRunE(root, nil); err != nil {
		t.Fatalf("PersistentPreRunE with no flags set must succeed, got %v", err)
	}
	if !root.SilenceUsage {
		t.Error("PersistentPreRun must set SilenceUsage: runtime errors (e.g. the Tier-1 stomp-guard report) must not be buried under a usage dump")
	}
}
