// Tests for the failed-run preservation + scaffold-once journal
// coverage added to the rollback journal.
//
// Contract pinned here:
//
//   - SnapshotJournalTargets copies the failed run's CURRENT output into
//     the preserve dir (mirroring relative paths) BEFORE RestoreRollback
//     rewinds the tree, skipping paths whose bytes did not change and
//     recreating the preserve dir fresh on every call.
//   - RecordPreWriteAbs journals a path addressed by a single joined
//     path (the scaffold-once raw writers' shape) against the armed
//     root, so a failed run's revert covers scaffold-once files written
//     this run too — the defect where handlers_crud.go survived while
//     handlers_crud_ops_gen.go was rolled back.
//   - Paths outside the armed root are never captured.
package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotJournalTargets_PreservesFailedOutputBeforeRestore(t *testing.T) {
	root := t.TempDir()
	preserveDir := filepath.Join(root, ".forge", "failed-generate")

	// Pre-run state: one file exists (will be overwritten), one absent
	// (will be created this run).
	const existingRel = "pkg/app/wire_gen.go"
	preRun := []byte("package app // pre-run\n")
	stampedPre, _ := Stamp(existingRel, preRun)
	if err := os.MkdirAll(filepath.Join(root, "pkg", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, existingRel), stampedPre, 0o644); err != nil {
		t.Fatal(err)
	}

	ResetSkipWrite()
	ResetPerRunState() // unscoped legacy force — the pipeline-writer shape
	t.Cleanup(ResetPerRunState)
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	const newRel = "internal/handlers/orders/handlers_crud_ops_gen.go"
	if _, err := WriteGeneratedFile(root, existingRel, []byte("package app // regen v2\n"), nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGeneratedFile(root, newRel, []byte("package orders // fresh ops\n"), nil, false); err != nil {
		t.Fatal(err)
	}

	failedExisting, _ := os.ReadFile(filepath.Join(root, existingRel))
	failedNew, _ := os.ReadFile(filepath.Join(root, newRel))

	preserved := SnapshotJournalTargets(root, preserveDir)
	if len(preserved) != 2 {
		t.Fatalf("preserved = %v, want both written paths", preserved)
	}

	// Preserved copies mirror relative paths and hold the FAILED bytes.
	gotExisting, err := os.ReadFile(filepath.Join(preserveDir, existingRel))
	if err != nil {
		t.Fatalf("preserved copy of %s missing: %v", existingRel, err)
	}
	if string(gotExisting) != string(failedExisting) {
		t.Errorf("preserved %s holds %q, want the failed-run bytes %q", existingRel, gotExisting, failedExisting)
	}
	gotNew, err := os.ReadFile(filepath.Join(preserveDir, newRel))
	if err != nil {
		t.Fatalf("preserved copy of %s missing: %v", newRel, err)
	}
	if string(gotNew) != string(failedNew) {
		t.Errorf("preserved %s holds %q, want the failed-run bytes %q", newRel, gotNew, failedNew)
	}

	// The restore still rewinds the real tree: preservation must never
	// weaken the clean-tree guarantee.
	RestoreRollback(root)
	afterExisting, _ := os.ReadFile(filepath.Join(root, existingRel))
	if string(afterExisting) != string(stampedPre) {
		t.Errorf("restore did not rewind %s to pre-run bytes", existingRel)
	}
	if _, err := os.Stat(filepath.Join(root, newRel)); !os.IsNotExist(err) {
		t.Errorf("restore should have deleted the newly-created %s, stat err = %v", newRel, err)
	}
	// The preserved copies survive the restore — that is their point.
	if _, err := os.Stat(filepath.Join(preserveDir, newRel)); err != nil {
		t.Errorf("preserved copy of %s must survive the restore: %v", newRel, err)
	}
}

func TestSnapshotJournalTargets_SkipsUnchangedAndRecreatesFresh(t *testing.T) {
	root := t.TempDir()
	preserveDir := filepath.Join(root, ".forge", "failed-generate")

	// A stale artifact from a PRIOR failed run must not survive into this
	// run's snapshot.
	if err := os.MkdirAll(preserveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preserveDir, "stale.go"), []byte("old failure\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const rel = "pkg/app/app_gen.go"
	content := []byte("package app // v1\n")
	stamped, _ := Stamp(rel, content)
	if err := os.MkdirAll(filepath.Join(root, "pkg", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	ResetSkipWrite()
	ResetPerRunState()
	t.Cleanup(ResetPerRunState)
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// An idempotent re-render: same bytes land on disk, so there is
	// nothing new to inspect and the path is skipped.
	if _, err := WriteGeneratedFile(root, rel, content, nil, true); err != nil {
		t.Fatal(err)
	}
	// And one path that DID change.
	const changedRel = "pkg/app/bootstrap.go"
	if _, err := WriteGeneratedFile(root, changedRel, []byte("package app // new\n"), nil, false); err != nil {
		t.Fatal(err)
	}

	preserved := SnapshotJournalTargets(root, preserveDir)
	if len(preserved) != 1 || preserved[0] != changedRel {
		t.Fatalf("preserved = %v, want exactly [%s] (unchanged path skipped)", preserved, changedRel)
	}
	if _, err := os.Stat(filepath.Join(preserveDir, "stale.go")); !os.IsNotExist(err) {
		t.Errorf("prior failed run's artifact must be cleared by the fresh snapshot, stat err = %v", err)
	}
}

func TestSnapshotJournalTargets_JournalOffIsNil(t *testing.T) {
	root := t.TempDir()
	CommitRollback() // journaling OFF
	if got := SnapshotJournalTargets(root, filepath.Join(root, ".forge", "failed-generate")); got != nil {
		t.Fatalf("SnapshotJournalTargets with journaling off = %v, want nil", got)
	}
}

func TestRecordPreWriteAbs_JournalsScaffoldOnceWriteForRestore(t *testing.T) {
	root := t.TempDir()

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// The scaffold-once raw-writer shape: a single joined path, written
	// with plain os.WriteFile after RecordPreWriteAbs.
	const rel = "internal/handlers/orders/handlers_crud.go"
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	RecordPreWriteAbs(full)
	if err := os.WriteFile(full, []byte("package orders // scaffold-once shim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restored := RestoreRollback(root)
	if len(restored) != 1 || filepath.ToSlash(restored[0]) != rel {
		t.Fatalf("restored = %v, want exactly [%s]", restored, rel)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("scaffold-once file written this run must be reverted with the run, stat err = %v", err)
	}
}

func TestRecordPreWriteAbs_OutsideRootNotCaptured(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	outsideFile := filepath.Join(outside, "elsewhere.go")
	RecordPreWriteAbs(outsideFile)
	if err := os.WriteFile(outsideFile, []byte("package elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if restored := RestoreRollback(root); len(restored) != 0 {
		t.Fatalf("a path outside the armed root must not be journaled, restored = %v", restored)
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside-root file must be untouched by the restore: %v", err)
	}
}
