package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// The reliant R2 defect, at the level it lives: forge scaffolded
// cmd/reliant/cmd/serve.go, serve.go switched composition on, composition
// failed, and the rollback took serve.go AND its birth record — so the
// project had no way to say "not this file" except hand-writing the ledger.
// A declined entry is that record, written outside the run; the run must
// neither write the file nor lose the decline when it rolls back.
func TestDeclinedScaffold_NeverWrittenAndSurvivesAFailedRun(t *testing.T) {
	root := absentChangeProject(t, nil, nil)
	const serve = "cmd/app/cmd/serve.go"
	DeclineScaffold(root, serve, "serve.go turns composition on; roots land later")

	BeginRollbackJournal(root)
	wrote, err := WriteScaffoldIfMissing(root, serve, []byte("package cmd\n"))
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatalf("a declined scaffold was written")
	}
	// Another scaffold IS born this run, so the ledger file is rewritten
	// and journaled — the restore must hand back the decline with it.
	if wrote, err := WriteScaffoldIfMissing(root, "cmd/app/cmd/version.go", []byte("package cmd\n")); err != nil || !wrote {
		t.Fatalf("ordinary birth: wrote=%v err=%v", wrote, err)
	}
	RestoreRollback(root) // the run failed after its writes

	if _, err := os.Stat(filepath.Join(root, serve)); !os.IsNotExist(err) {
		t.Fatalf("declined %s exists after the failed run (stat err %v)", serve, err)
	}
	if why, ok := ScaffoldDeclined(root, serve); !ok || why == "" {
		t.Fatalf("the failed run lost the decline: ok=%v reason=%q", ok, why)
	}
	if ScaffoldRecorded(root, "cmd/app/cmd/version.go") {
		t.Fatalf("a birth the rollback un-created kept its record — it would read as deliberately deleted")
	}
	// The decline also survives a fresh read of the file, not just the cache.
	ResetScaffoldLedgerCache()
	if _, ok := ScaffoldDeclined(root, serve); !ok {
		t.Fatalf("decline was not persisted to %s", ScaffoldedFile)
	}
	if ScaffoldOnceDecision(root, serve) {
		t.Fatalf("ScaffoldOnceDecision would birth a declined path")
	}
}

// A decline is a declaration, not a deletion: the absent-scaffolds notice
// (which exists for the moment a scaffold goes MISSING) must not report it.
func TestDeclinedScaffold_IsNotReportedAbsent(t *testing.T) {
	root := absentChangeProject(t, []string{"gone.go"}, nil)
	DeclineScaffold(root, "declined.go", "not wanted")
	if got := AbsentScaffolds(root); len(got) != 1 || got[0] != "gone.go" {
		t.Fatalf("AbsentScaffolds = %v, want [gone.go] (a declined path is not a deletion)", got)
	}
	// Re-adoption is dropping the entry, exactly as for a deletion.
	ForgetScaffold(root, "declined.go")
	if !ScaffoldOnceDecision(root, "declined.go") {
		t.Fatalf("a forgotten decline must be scaffoldable again")
	}
}

// A scaffold-once file is never half-recorded: when the rollback cannot
// remove a file this run created, the file's birth record must stay with
// it. Restoring the ledger alone left the file on disk unrecorded, so the
// user's later deletion of it read as "never scaffolded" and forge wrote it
// back.
func TestRollback_BirthWhoseFileCannotBeRemovedKeepsItsRecord(t *testing.T) {
	root := absentChangeProject(t, nil, nil)
	const rel = "internal/metrics/metrics.go"

	BeginRollbackJournal(root)
	if wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package metrics\n")); err != nil || !wrote {
		t.Fatalf("birth: wrote=%v err=%v", wrote, err)
	}
	// Make the removal fail: a non-empty directory where the file was.
	full := filepath.Join(root, rel)
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(full, "pinned"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "pinned", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, failed := RestoreRollbackReport(root)
	if len(failed) == 0 {
		t.Fatalf("fixture did not make the removal fail")
	}
	ResetScaffoldLedgerCache()
	if !ScaffoldRecorded(root, rel) {
		t.Fatalf("%s survived the rollback but its birth record did not — half-recorded", rel)
	}
}

// An adoption (a present, unrecorded file from an older forge) is recorded
// during a run but is not a birth of THIS run: a rollback must leave the
// ledger at its pre-run bytes rather than re-recording it.
func TestRollback_AdoptionIsNotReconciledBackIn(t *testing.T) {
	root := absentChangeProject(t, []string{"seed.go"}, []string{"old.go", "seed.go"})
	before, err := os.ReadFile(filepath.Join(root, ScaffoldedFile))
	if err != nil {
		t.Fatal(err)
	}
	BeginRollbackJournal(root)
	if ScaffoldOnceDecision(root, "old.go") {
		t.Fatalf("a present file is never a birth")
	}
	if got := ScaffoldsCreatedThisRun(); len(got) != 0 {
		t.Fatalf("ScaffoldsCreatedThisRun = %v, want none (an adoption creates no file)", got)
	}
	RestoreRollback(root)
	after, err := os.ReadFile(filepath.Join(root, ScaffoldedFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ledger differs from its pre-run bytes after a rollback:\nbefore %s\nafter  %s", before, after)
	}
}
