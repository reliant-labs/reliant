// Tests for the stage-then-validate rollback journal (fr-40f7ec9bd9).
//
// Contract pinned here:
//
//   - Begin arms journaling; every forge write through the chokepoint
//     captures its target's pre-run bytes exactly once (first-write wins).
//   - RestoreRollback rewinds: a file that existed pre-run is restored to
//     its original bytes; a file that did NOT exist pre-run is deleted,
//     and the now-empty parent dirs forge created are pruned.
//   - CommitRollback lets the writes stand and disarms journaling.
//   - With journaling OFF (non-pipeline callers), writes are recorded
//     nowhere and RestoreRollback is a no-op.
package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollback_RestoresModifiedFileToPreRunBytes(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/bootstrap.go"
	preRun := []byte("package app // committed HEAD\n")
	stampedPre, _ := Stamp(rel, preRun)
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stampedPre, 0o644); err != nil {
		t.Fatal(err)
	}

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// forge regenerates the file (a newer vintage) — this is the write the
	// failed run must undo.
	if _, err := WriteGeneratedFile(root, rel, []byte("package app // regenerated v2\n"), nil, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(full); string(got) == string(stampedPre) {
		t.Fatal("precondition: regen should have changed the file on disk")
	}

	restored := RestoreRollback(root)
	if len(restored) != 1 || restored[0] != rel {
		t.Fatalf("restored = %v, want exactly [%s]", restored, rel)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(stampedPre) {
		t.Fatalf("rollback did not restore pre-run bytes:\n got: %q\nwant: %q", got, stampedPre)
	}
}

func TestRollback_DeletesNewlyCreatedFileAndPrunesEmptyDirs(t *testing.T) {
	root := t.TempDir()
	const rel = "internal/db/widget_orm_gen.go" // dir does NOT exist pre-run

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	if _, err := WriteGeneratedFile(root, rel, []byte("package db // fresh\n"), nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("precondition: file should exist after write: %v", err)
	}

	restored := RestoreRollback(root)
	if len(restored) != 1 || restored[0] != rel {
		t.Fatalf("restored = %v, want exactly [%s]", restored, rel)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
		t.Fatalf("rollback should have deleted the newly-created file, stat err = %v", err)
	}
	// The internal/db directory forge created should be pruned away.
	if _, err := os.Stat(filepath.Join(root, "internal", "db")); !os.IsNotExist(err) {
		t.Fatalf("rollback should have pruned the empty dir forge created, stat err = %v", err)
	}
}

func TestRollback_PruneStopsAtNonEmptyDir(t *testing.T) {
	root := t.TempDir()
	// A sibling user file in internal/ keeps that dir non-empty after the
	// generated file under internal/db/ is deleted — prune must stop there.
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "keep.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	const rel = "internal/db/widget_orm_gen.go"
	if _, err := WriteGeneratedFile(root, rel, []byte("package db\n"), nil, false); err != nil {
		t.Fatal(err)
	}
	RestoreRollback(root)

	if _, err := os.Stat(filepath.Join(root, "internal", "keep.go")); err != nil {
		t.Fatalf("prune must not remove a non-empty ancestor's sibling file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "db")); !os.IsNotExist(err) {
		t.Fatalf("the emptied internal/db should still be pruned: %v", err)
	}
}

func TestRollback_FirstCaptureWins(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/wire_gen.go"
	preRun := []byte("package app // HEAD\n")
	stampedPre, _ := Stamp(rel, preRun)
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stampedPre, 0o644); err != nil {
		t.Fatal(err)
	}

	ResetSkipWrite()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// Two writes to the same path in one run (e.g. an emitter then a
	// re-emit). The journal must hold the ORIGINAL pre-run bytes, not the
	// intermediate first-write output.
	if _, err := WriteGeneratedFile(root, rel, []byte("package app // intermediate\n"), nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGeneratedFile(root, rel, []byte("package app // final\n"), nil, true); err != nil {
		t.Fatal(err)
	}
	RestoreRollback(root)

	got, _ := os.ReadFile(full)
	if string(got) != string(stampedPre) {
		t.Fatalf("first-capture-wins violated:\n got: %q\nwant: %q", got, stampedPre)
	}
}

func TestRollback_CommitLeavesWritesStanding(t *testing.T) {
	root := t.TempDir()
	const rel = "cmd/services_gen.go"

	ResetSkipWrite()
	BeginRollbackJournal(root)

	if _, err := WriteGeneratedFile(root, rel, []byte("package main // generated\n"), nil, false); err != nil {
		t.Fatal(err)
	}
	CommitRollback()
	if RollbackEnabled() {
		t.Fatal("CommitRollback should disarm journaling")
	}
	// After commit, a restore call must be inert (journal dropped).
	if restored := RestoreRollback(root); restored != nil {
		t.Fatalf("RestoreRollback after commit should be a no-op, got %v", restored)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("committed write should remain on disk: %v", err)
	}
}

func TestRollback_DisabledIsNoOp(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/app_gen.go"

	ResetSkipWrite()
	CommitRollback() // ensure journaling is OFF (non-pipeline caller)
	if RollbackEnabled() {
		t.Fatal("journaling should be OFF after CommitRollback")
	}

	if _, err := WriteGeneratedFile(root, rel, []byte("package app\n"), nil, false); err != nil {
		t.Fatal(err)
	}
	if restored := RestoreRollback(root); restored != nil {
		t.Fatalf("RestoreRollback with journaling OFF must be a no-op, got %v", restored)
	}
	// The write must survive — nothing to roll back to.
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("write with journaling off should stand: %v", err)
	}
}
