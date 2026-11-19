// Revert-set consistency for the two writer tiers (the defect-2 fix).
//
// A real failed `forge scaffold` run left the tree in the WORST
// possible intermediate state: the Tier-1 handlers_crud_ops_gen.go was
// reverted by the failed run's rollback, but the scaffold-once
// handlers_crud.go shim written THE SAME RUN survived — because the
// scaffold-once raw writers (writeUserScaffold / writeUserScaffoldIfAbsent)
// bypassed the rollback journal. The surviving shim referenced ops that
// no longer existed, so a post-failure `go build ./...` produced 20+
// red-herring "undefined" errors pointing at code forge itself wrote.
//
// Pinned contract: a run that writes shim + ops and then fails must
// leave NEITHER on disk after the revert, and must leave BOTH under the
// preserve directory (.forge/failed-generate/ in the pipeline).
package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestFailedRunRevert_ScaffoldShimAndTier1OpsRevertTogether(t *testing.T) {
	root := t.TempDir()
	relDir := filepath.Join("internal", "handlers", "orders")
	if err := os.MkdirAll(filepath.Join(root, relDir), 0o755); err != nil {
		t.Fatal(err)
	}

	shimRel := filepath.Join(relDir, "handlers_crud.go")
	opsRel := filepath.Join(relDir, "handlers_crud_ops_gen.go")

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	// The two writes a CRUD projection performs, through the REAL writer
	// helpers: the Tier-1 ops file (chokepoint, force=true) and the
	// scaffold-once user shim (raw write).
	if err := writeForgeOwned(root, opsRel, []byte("package orders // ops (tier-1)\n"), &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	if err := writeUserScaffold(filepath.Join(root, shimRel), []byte("package orders // shim (scaffold-once)\n")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{shimRel, opsRel} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("precondition: %s should exist after the writes: %v", rel, err)
		}
	}

	// The run fails validation: preserve, then revert.
	preserveDir := filepath.Join(root, ".forge", "failed-generate")
	preserved := checksums.SnapshotJournalTargets(root, preserveDir)
	restored := checksums.RestoreRollback(root)

	if len(restored) != 2 {
		t.Fatalf("restored = %v, want BOTH the shim and the ops file — a mixed revert is the defect", restored)
	}
	for _, rel := range []string{shimRel, opsRel} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s must be reverted with the failed run (stat err = %v) — a surviving half references code that was rolled back", rel, err)
		}
		if _, err := os.Stat(filepath.Join(preserveDir, rel)); err != nil {
			t.Errorf("%s must be preserved under the failed-generate dir so nothing the run produced is lost: %v", rel, err)
		}
	}
	if len(preserved) != 2 {
		t.Errorf("preserved = %v, want both files", preserved)
	}
}

// TestFailedRunRevert_PreExistingScaffoldContentRestoredNotDeleted: the
// revert must never destroy PRE-EXISTING user content. A shim that
// existed before the run and was APPENDED to this run (the
// ensureCRUDShimFile append path goes through writeUserScaffold with the
// full new content) is restored to its pre-run bytes — the appended
// shims are undone, the user's original content survives.
func TestFailedRunRevert_PreExistingScaffoldContentRestoredNotDeleted(t *testing.T) {
	root := t.TempDir()
	relDir := filepath.Join("internal", "handlers", "orders")
	if err := os.MkdirAll(filepath.Join(root, relDir), 0o755); err != nil {
		t.Fatal(err)
	}
	shimRel := filepath.Join(relDir, "handlers_crud.go")
	full := filepath.Join(root, shimRel)

	userContent := []byte("package orders // the user's hand-maintained shim file\n")
	if err := os.WriteFile(full, userContent, 0o644); err != nil {
		t.Fatal(err)
	}

	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(root)
	t.Cleanup(checksums.CommitRollback)

	appended := append(append([]byte{}, userContent...), []byte("\nfunc appendedThisRun() {}\n")...)
	if err := writeUserScaffold(full, appended); err != nil {
		t.Fatal(err)
	}

	checksums.SnapshotJournalTargets(root, filepath.Join(root, ".forge", "failed-generate"))
	checksums.RestoreRollback(root)

	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("pre-existing user file must never be deleted by the revert: %v", err)
	}
	if string(got) != string(userContent) {
		t.Errorf("revert must restore the user's pre-run bytes:\n got: %q\nwant: %q", got, userContent)
	}
}

// TestScaffoldWritersInertWhenJournalOff pins the non-pipeline contract:
// with journaling off (project new, upgrade), the scaffold writers do
// not record anywhere and a later RestoreRollback is a no-op.
func TestScaffoldWritersInertWhenJournalOff(t *testing.T) {
	root := t.TempDir()
	checksums.CommitRollback() // journaling OFF

	full := filepath.Join(root, "internal", "app", "compose.go")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if wrote, err := writeUserScaffoldIfAbsent(full, []byte("package app\n")); err != nil || !wrote {
		t.Fatalf("writeUserScaffoldIfAbsent = (%v, %v), want fresh write", wrote, err)
	}
	if restored := checksums.RestoreRollback(root); restored != nil {
		t.Fatalf("journaling off: restore must be a no-op, got %v", restored)
	}
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("scaffold write with journaling off must stand: %v", err)
	}
}
