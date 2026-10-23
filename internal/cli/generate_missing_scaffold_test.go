// Tests for the missing scaffold-once artifact notice
// (generate_missing_scaffold.go).
//
// All cases are pure-filesystem in t.TempDir() — no subprocesses, no
// postgres, no network — so the file runs unconditionally in -short mode.
//
// The assertions are about the two facts the measured run needed and did
// not get: WHICH file, and WHAT action restores it. They deliberately do
// not pin the full prose (that would make every wording improvement a test
// edit); they pin that the file and the remedy are present, because a
// notice missing either one costs the same hour the original defect did.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// newLedgerProject makes a temp dir that reads as a project root (the
// ledger walks up to the nearest forge.yaml) with a clean ledger cache.
//
// Tests using it must NOT call t.Parallel(). The birth ledger's in-memory
// copy is a process-global cache (checksums.scaffoldLedgers), unguarded
// because forge's own callers are a single-threaded CLI run; concurrent
// tests race it into a "concurrent map writes" panic. Every other test in
// the tree that touches the ledger is serial for the same reason.
func newLedgerProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "forge.yaml"), "name: ledgertest\n")
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	return root
}

// TestMissingScaffoldNotice_NamesFileAndAction reproduces the run's exact
// shape: a born CRUD lifecycle test the author deleted after correcting the
// migrations its fixtures were derived from. Generate must say so, name the
// file, and name the one edit that brings it back.
//
// Without the notice this run got a success banner and silence, then spent
// ~14 tool calls trying to reproduce the birth condition by hand.
func TestMissingScaffoldNotice_NamesFileAndAction(t *testing.T) {
	root := newLedgerProject(t)
	const rel = "internal/handlers/library/handlers_crud_test.go"
	if wrote, err := checksums.WriteScaffoldIfMissing(root, rel, []byte("package library\n")); err != nil || !wrote {
		t.Fatalf("seed scaffold birth (wrote=%v, err=%v)", wrote, err)
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("delete the scaffolded test: %v", err)
	}

	var out bytes.Buffer
	if !reportMissingScaffolds(&out, root) {
		t.Fatal("reportMissingScaffolds reported NOTHING for a scaffold-once file " +
			"forge has written before and that is now absent. That silence is the " +
			"defect: the run deleted this file expecting `forge generate` to re-derive " +
			"it against the migrations it had just corrected, generate succeeded " +
			"quietly, and nothing in the output named the file or the edit that " +
			"restores it.")
	}
	got := out.String()

	if !strings.Contains(got, rel) {
		t.Errorf("notice does not NAME the absent file %q — a notice that says "+
			"something drifted without naming the file is not actionable:\n%s", rel, got)
	}
	// The remedy is a COMMAND, runnable verbatim for the one absent file.
	// Deleting the file, the proto rpcs, db/migrations or the handler dir
	// all fail to bring it back — the run tried each.
	if want := "project rescaffold " + rel; !strings.Contains(got, want) {
		t.Errorf("notice does not name the command that re-creates the file (%q):\n%s", want, got)
	}
	if strings.Contains(got, "jq ") {
		t.Errorf("notice still tells the reader to hand-edit %s:\n%s", checksums.ScaffoldedFile, got)
	}
}

// TestMissingScaffoldNotice_SilentWhenPresent is the noise guard. A healthy
// project — every scaffold-once file still on disk — must produce no notice
// at all. A notice that fires on every run is one users and agents learn to
// skip, which is how the real signal gets missed.
func TestMissingScaffoldNotice_SilentWhenPresent(t *testing.T) {
	root := newLedgerProject(t)
	for _, rel := range []string{"internal/app/auth.go", "internal/handlers/library/handlers_crud_test.go"} {
		if wrote, err := checksums.WriteScaffoldIfMissing(root, rel, []byte("package x\n")); err != nil || !wrote {
			t.Fatalf("seed scaffold %s (wrote=%v, err=%v)", rel, wrote, err)
		}
	}

	var out bytes.Buffer
	if reportMissingScaffolds(&out, root) {
		t.Fatalf("reported a notice for a project where every scaffold is present:\n%s", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("wrote output while reporting nothing:\n%s", out.String())
	}
}

// TestMissingScaffoldNotice_GeneralAcrossWriters pins the design constraint
// that this is NOT a special case for handlers_crud_test.go. Forge writes
// many scaffold-once files and any of them can be deleted for the same
// reason; the notice is keyed on the ledger, so a writer added later is
// covered without being taught about.
func TestMissingScaffoldNotice_GeneralAcrossWriters(t *testing.T) {
	root := newLedgerProject(t)
	// Three different writers' outputs: the auth scaffold, the command
	// tree's main, and a frontend page. None is the CRUD lifecycle test.
	absent := []string{
		"cmd/app/main.go",
		"frontends/web/src/app/page.tsx",
		"internal/app/auth.go",
	}
	for _, rel := range absent {
		if wrote, err := checksums.WriteScaffoldIfMissing(root, rel, []byte("x\n")); err != nil || !wrote {
			t.Fatalf("seed scaffold %s (wrote=%v, err=%v)", rel, wrote, err)
		}
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatalf("delete %s: %v", rel, err)
		}
	}

	var out bytes.Buffer
	if !reportMissingScaffolds(&out, root) {
		t.Fatal("reported nothing for three deleted scaffold-once files")
	}
	got := out.String()
	for _, rel := range absent {
		if !strings.Contains(got, rel) {
			t.Errorf("notice omits %q — the check must be general over the ledger, "+
				"not special-cased to the one file a single run happened to hit:\n%s", rel, got)
		}
	}
}

// TestMissingScaffoldNotice_EmptyRendersNothing pins the render function's
// own contract independently of the filesystem: no paths ⇒ empty string,
// so callers can print unconditionally without emitting a blank block.
func TestMissingScaffoldNotice_EmptyRendersNothing(t *testing.T) {
	t.Parallel()
	if got := missingScaffoldNotice(nil); got != "" {
		t.Errorf("missingScaffoldNotice(nil) = %q, want empty", got)
	}
}

// TestRescaffoldHint_ConcreteOnlyWhenUnambiguous pins that the remedy
// command names a real path when exactly one file is absent (an agent can
// run it verbatim), and a placeholder when several are — rather than
// confidently naming whichever sorts first, which is rarely the file the
// author just deleted. A remedy pointing at the wrong file is worse than
// one asking for a substitution, given the hour the original defect cost
// was spent hunting for the remedy in the first place.
func TestRescaffoldHint_ConcreteOnlyWhenUnambiguous(t *testing.T) {
	t.Parallel()
	const one = "internal/handlers/library/handlers_crud_test.go"
	if got := rescaffoldHint([]string{one}); !strings.Contains(got, one) {
		t.Errorf("single-path hint should be runnable verbatim, got: %s", got)
	}

	two := []string{"internal/app/auth.go", one}
	got := rescaffoldHint(two)
	if !strings.Contains(got, "<path>") {
		t.Errorf("multi-path hint must use a placeholder, got: %s", got)
	}
	for _, p := range two {
		if strings.Contains(got, p) {
			t.Errorf("multi-path hint silently picked %q; with several absent files "+
				"forge cannot know which one the author wants back:\n%s", p, got)
		}
	}
}
