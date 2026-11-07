package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteScaffoldIfMissing_WritesWhenAbsent: the first write scaffolds
// the file (no marker — user-owned from birth).
func TestWriteScaffoldIfMissing_WritesWhenAbsent(t *testing.T) {
	ResetSkipWrite()

	root := t.TempDir()

	scaffold := []byte("// scaffold\npackage svc\n")
	wrote, err := WriteScaffoldIfMissing(root, "svc.go", scaffold)
	if err != nil || !wrote {
		t.Fatalf("initial scaffold write: wrote=%v err=%v", wrote, err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(root, "svc.go"))
	if string(onDisk) != string(scaffold) {
		t.Errorf("scaffold content = %q, want %q", onDisk, scaffold)
	}
	if Verify(onDisk) != NoMarker {
		t.Fatalf("scaffold must carry no marker; got %q", onDisk)
	}
}

// TestWriteScaffoldIfMissing_NeverOverwritesExisting is the headline
// contract: once the file exists forge NEVER overwrites it — no flag, no
// exception. A hand-edit, a pristine prior render, or any content on disk
// is preserved verbatim.
func TestWriteScaffoldIfMissing_NeverOverwritesExisting(t *testing.T) {
	ResetSkipWrite()

	root := t.TempDir()

	// Seed a scaffold, then hand-edit it.
	scaffold := []byte("// scaffold\npackage svc\n")
	if _, err := WriteScaffoldIfMissing(root, "svc.go", scaffold); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte("// user code\npackage svc\n\nfunc Important() {}\n")
	if err := os.WriteFile(filepath.Join(root, "svc.go"), handEdited, 0o644); err != nil {
		t.Fatal(err)
	}

	// A subsequent scaffold write must be a no-op that preserves the edit.
	wrote, err := WriteScaffoldIfMissing(root, "svc.go", []byte("// new template\npackage svc\n"))
	if err != nil {
		t.Fatalf("re-write: %v", err)
	}
	if wrote {
		t.Errorf("scaffold write overwrote an existing file (contract violated)")
	}
	got, _ := os.ReadFile(filepath.Join(root, "svc.go"))
	if string(got) != string(handEdited) {
		t.Errorf("hand-edits not preserved; got:\n%s\nwant:\n%s", got, handEdited)
	}
}

// TestWriteScaffoldIfMissing_DeleteIsNotTheRefreshPath: deleting a
// scaffold is a REMOVAL, not a request for a fresh copy.
//
// This test previously asserted delete-and-regenerate as "the documented
// refresh path". That documentation was the defect — it made the promise
// in every scaffold's banner ("forge will not overwrite this file") false
// for the one user action that most clearly expresses not wanting the
// file. The refresh path is now dropping the path's entry from the birth
// ledger, which this test also exercises so the fix cannot leave deleted
// scaffolds permanently unrecoverable.
func TestWriteScaffoldIfMissing_DeleteIsNotTheRefreshPath(t *testing.T) {
	ResetSkipWrite()
	ResetScaffoldLedgerCache()
	defer ResetScaffoldLedgerCache()

	root := t.TempDir()
	scaffold := []byte("// v1\npackage svc\n")
	if _, err := WriteScaffoldIfMissing(root, "svc.go", scaffold); err != nil {
		t.Fatal(err)
	}
	// Hand-edit, then delete.
	if err := os.WriteFile(filepath.Join(root, "svc.go"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "svc.go")); err != nil {
		t.Fatal(err)
	}

	refreshed := []byte("// v2\npackage svc\n")
	wrote, err := WriteScaffoldIfMissing(root, "svc.go", refreshed)
	if err != nil {
		t.Fatalf("regenerate after delete: %v", err)
	}
	if wrote {
		t.Fatal("deleting a scaffold must not refresh it — the deletion is the " +
			"user's decision and forge must leave it standing")
	}

	// The real reset: forget the birth, and the scaffold returns.
	ForgetScaffold(root, "svc.go")
	wrote, err = WriteScaffoldIfMissing(root, "svc.go", refreshed)
	if err != nil || !wrote {
		t.Fatalf("dropping the ledger entry is the documented refresh path "+
			"(wrote=%v err=%v)", wrote, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "svc.go"))
	if string(got) != string(refreshed) {
		t.Errorf("refreshed content = %q, want %q", got, refreshed)
	}
}

// TestWriteScaffoldIfMissing_CreatesParentDirs: parents are created as
// needed when the destination sits in a not-yet-existing directory.
func TestWriteScaffoldIfMissing_CreatesParentDirs(t *testing.T) {
	ResetSkipWrite()

	root := t.TempDir()
	rel := filepath.Join("internal", "db", "widget_repo_ext.go")
	wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package db\n"))
	if err != nil || !wrote {
		t.Fatalf("scaffold into nested dir: wrote=%v err=%v", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("scaffold not written into created parents: %v", err)
	}
}

// TestWriteGeneratedFileTier1_ForceStillOverwrites verifies the unchanged
// Tier-1 contract: Tier-1 + --force continues to clobber hand-edits.
// Tier-1 IS "regenerated every run, DO NOT EDIT" — that contract didn't
// move when the scaffold tier collapsed.
func TestWriteGeneratedFileTier1_ForceStillOverwrites(t *testing.T) {
	ResetSkipWrite()
	ResetPerRunState() // unscoped force
	defer ResetPerRunState()

	root := t.TempDir()
	cs := &FileChecksums{}

	initial := []byte("// generated\npackage handlers\n")
	_, _ = WriteGeneratedFileTier1(root, "handlers_gen.go", initial, cs, false)

	// User hand-edit that keeps the marker (a wholesale replace would
	// drop the marker and become the untracked-overwrite path instead).
	stamped, _ := os.ReadFile(filepath.Join(root, "handlers_gen.go"))
	if err := os.WriteFile(filepath.Join(root, "handlers_gen.go"), append(stamped, []byte("// user\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without force the stomp guard skips it…
	newTemplate := []byte("// regen\npackage handlers\n")
	wrote, err := WriteGeneratedFileTier1(root, "handlers_gen.go", newTemplate, cs, false)
	if err != nil {
		t.Fatalf("Tier-1 unforced write: %v", err)
	}
	if wrote {
		t.Error("hand-edited Tier-1 file overwritten without --force")
	}

	// …--force on a Tier-1 file overwrites — unchanged behavior.
	wrote, err = WriteGeneratedFileTier1(root, "handlers_gen.go", newTemplate, cs, true)
	if err != nil {
		t.Fatalf("Tier-1 force write: %v", err)
	}
	if !wrote {
		t.Error("force=true on a hand-edited Tier-1 file did NOT overwrite (regression)")
	}
	got, _ := os.ReadFile(filepath.Join(root, "handlers_gen.go"))
	wantStamped, _ := Stamp("handlers_gen.go", newTemplate)
	if string(got) != string(wantStamped) {
		t.Errorf("Tier-1 not overwritten+stamped under --force; got:\n%s", got)
	}
}
