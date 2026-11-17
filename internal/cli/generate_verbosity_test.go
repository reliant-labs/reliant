package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoutineLinesSilentByDefault is the headline behaviour: a line that
// reports forge correctly doing nothing does not print unless asked for.
//
// Before the change these were unconditional fmt.Printf calls, so this
// failed — control-plane printed 22 "all handlers up to date" lines and ~25
// "Skipped contract_test.go scaffold" lines on a clean, zero-change run.
func TestRoutineLinesSilentByDefault(t *testing.T) {
	defer setGenerateVerbosity(false)()
	got := captureStdout(t, func() {
		routinef("  ⏭️  Skipped internal/handlers/user/ (all handlers up to date)\n")
	})
	if got != "" {
		t.Fatalf("routine line printed at default verbosity; `forge generate` must report only progress and "+
			"things needing action. got: %q", got)
	}
}

// TestRoutineLinesPrintUnderVerbose is the other half of the contract: the
// information is suppressed, not destroyed. "Why didn't generate touch X?"
// must still be answerable without reading forge's source.
func TestRoutineLinesPrintUnderVerbose(t *testing.T) {
	defer setGenerateVerbosity(true)()
	got := captureStdout(t, func() {
		routinef("  ⏭️  Skipped internal/handlers/user/ (all handlers up to date)\n")
	})
	if !strings.Contains(got, "all handlers up to date") {
		t.Fatalf("routine line missing under -v; the detail must remain reachable on demand. got: %q", got)
	}
}

// TestSetGenerateVerbosityRestores pins the restore, which matters because
// the reliant harness runs forge's cobra tree IN-PROCESS: without it, one
// `generate -v` would leave every later generate in the same process verbose.
func TestSetGenerateVerbosityRestores(t *testing.T) {
	restore := setGenerateVerbosity(true)
	if !generateVerbose {
		t.Fatal("setGenerateVerbosity(true) did not take effect")
	}
	restore()
	if generateVerbose {
		t.Fatal("verbosity leaked past the run that set it; an in-process embedding would inherit it")
	}
}

// TestContractTestDeclinesCollapseToOneLine is the polish-hint decision.
//
// `func New(Deps) (Service, error)` IS forge's canonical shape and polishing
// to it re-enables the auto-scaffold, so this advice is real and survives at
// default verbosity. What does not survive is one line per package: 22 copies
// of the same sentence is not 22 times the advice.
func TestContractTestDeclinesCollapseToOneLine(t *testing.T) {
	d := &contractTestDeclines{}
	for _, pkg := range []string{"internal/litellm", "internal/billing", "internal/email", "internal/planlimits"} {
		d.notePolish(pkg)
	}

	// Under -v it is ONE line with a count and an example — not one line
	// per package, which is the spam this collapses.
	summary := d.polishSummary()
	if !strings.Contains(summary, "4 package(s)") {
		t.Errorf("summary must carry the COUNT so the reader knows the scale without the list; got %q", summary)
	}
	// Alphabetically first, so the named example is stable across runs
	// rather than depending on directory walk order.
	if !strings.Contains(summary, "internal/billing/") {
		t.Errorf("summary must name a concrete package so it is actionable on its own; got %q", summary)
	}
	if strings.Contains(summary, "internal/litellm") {
		t.Errorf("summary listed every package — that is the per-package spam this collapses; got %q", summary)
	}

	// The whole report at default verbosity is exactly that one line.
	// At default verbosity the whole report is silent: the advice is real,
	// but it is a STANDING fact (these packages have the shape their
	// authors chose) and would print unchanged on every run.
	defer setGenerateVerbosity(false)()
	if out := captureStdout(t, d.report); out != "" {
		t.Errorf("default-verbosity report is not silent:\n%s", out)
	}
}

// TestContractTestDeclinesRoutineAreSilentByDefault separates advice from
// non-advice. A multi-interface package is a deliberate, stable shape — there
// is nothing to act on, so it is routine.
func TestContractTestDeclinesRoutineAreSilentByDefault(t *testing.T) {
	d := &contractTestDeclines{}
	d.note("internal/audit", "multi-interface package; write tests manually")
	d.note("internal/coupon", "multi-interface package; write tests manually")

	defer setGenerateVerbosity(false)()
	if out := captureStdout(t, d.report); out != "" {
		t.Fatalf("multi-interface declines printed at default verbosity; they carry no action. got: %q", out)
	}

	if lines := d.routineLines(); len(lines) != 2 {
		t.Fatalf("routine declines must still be RECORDED for -v; got %d", len(lines))
	}
}

// TestScaffoldSkipLineRoutineOnlyWhenPresent guards the one split that is
// easy to get wrong: both branches of scaffoldSkipLine start "⏭️  Skipped",
// but only one is routine.
//
// "exists — yours to edit" is identical on every run and reports forge
// leaving a file alone. "absent, but deleted by you" names a MISSING file
// and the command that restores it — the sentence a measured run spent an
// hour failing to find. Suppressing the second would re-open that bug, so
// this asserts the classification, not the wording.
func TestScaffoldSkipLineRoutineOnlyWhenPresent(t *testing.T) {
	root := t.TempDir()
	const rel = ".github/workflows/ci.yml"

	// Both branches are routine — each is identical on every run. What the
	// deleted branch must never do is claim the file EXISTS, because that
	// sends the reader hunting for a bug in the wrong place.
	line, routine := scaffoldSkipLine(root, rel)
	if !routine {
		t.Error("a deleted scaffold-once file's line is identical on every run, so it is routine; " +
			"the EVENT is reported by reportMissingScaffolds, which names the same command")
	}
	if strings.Contains(line, "exists") {
		t.Errorf("the line claims a DELETED file exists: %q", line)
	}
	if !strings.Contains(line, "rescaffold") {
		t.Errorf("the line must still name the command that restores it: %q", line)
	}

	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("on: push\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	presentLine, presentRoutine := scaffoldSkipLine(root, rel)
	if !presentRoutine {
		t.Errorf("a PRESENT scaffold-once file is routine — forge did nothing and will do nothing next run: %q", presentLine)
	}
	if !strings.Contains(presentLine, "exists") {
		t.Errorf("a present file's line should say so: %q", presentLine)
	}
}

// TestContractTestDeclinesVerboseListsEverything asserts -v restores the full
// per-package detail on both sets.
func TestContractTestDeclinesVerboseListsEverything(t *testing.T) {
	d := &contractTestDeclines{}
	d.note("internal/audit", "multi-interface package; write tests manually")
	d.notePolish("internal/litellm")
	d.notePolish("internal/billing")

	defer setGenerateVerbosity(true)()
	out := captureStdout(t, d.report)
	for _, want := range []string{"internal/audit", "internal/litellm", "internal/billing"} {
		if !strings.Contains(out, want) {
			t.Errorf("-v output is missing %s; the detail must be reachable on demand:\n%s", want, out)
		}
	}
}
