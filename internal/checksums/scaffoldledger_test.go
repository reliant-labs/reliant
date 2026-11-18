// Tests for the scaffold-once BIRTH LEDGER.
//
// The defect these pin: a scaffold-once file whose banner promises "forge
// will not overwrite this file" came back on every `forge generate` after
// the user deleted it, because the guard asked os.Stat (a two-state
// question) instead of the ledger (the three-state one).
//
// DERIVATION. None of these assert on message text or grep output. The
// central assertions are derived from the producer itself:
//
//   - the SET of scaffold-once writer call sites is derived by parsing the
//     Go AST of internal/codegen + internal/checksums and finding the
//     functions that gate a write, rather than from a hand-listed table
//     that would silently stop covering a writer added later;
//   - the behavioral tests read WriteScaffoldIfMissing's own returned
//     `wrote` boolean — the producer's report of what it did — rather than
//     inferring it from the file's presence afterward.
//
// Empty derived sets FAIL LOUDLY: a discovery walk that finds nothing has
// broken its own premise (a moved package, a renamed helper), and silently
// passing on zero cases is how a guard rots into decoration.
package checksums

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// newScaffoldProject makes a temp dir that SplitScaffoldPath will recognize
// as a project root (it walks up to the nearest forge.yaml) and clears the
// process-global ledger cache so each test starts from disk.
func newScaffoldProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: scaffoldtest\n"), 0o644); err != nil {
		t.Fatalf("seed forge.yaml: %v", err)
	}
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)
	return root
}

// TestScaffoldOnce_DeletionSticks is the regression test for the reported
// defect: scaffold, delete, regenerate — it must stay deleted.
//
// The assertion derives from WriteScaffoldIfMissing's OWN return value
// (`wrote`), which is the producer stating whether it performed a birth.
// Checking only "is the file on disk?" would pass against a writer that
// re-created the file and then had it removed by something else.
func TestScaffoldOnce_DeletionSticks(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/handlers/storefront/handlers_crud_test.go"
	body := []byte("package storefront // lifecycle test\n")

	// Birth.
	wrote, err := WriteScaffoldIfMissing(root, rel, body)
	if err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if !wrote {
		t.Fatal("first scaffold into an empty project must WRITE (wrote=false): " +
			"the birth path is broken, which is a worse defect than the one under test")
	}

	// The user exercises ownership by deleting it — the author whose
	// CHECK-constrained migrations the scaffolded fixtures cannot satisfy.
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("delete scaffold: %v", err)
	}

	// Regenerate, repeatedly — the real run deleted and regenerated three
	// times (15:44, 15:48, 15:49) and got the file back every time.
	for i := 2; i <= 4; i++ {
		wrote, err := WriteScaffoldIfMissing(root, rel, body)
		if err != nil {
			t.Fatalf("regenerate #%d: %v", i, err)
		}
		if wrote {
			t.Fatalf("regenerate #%d RE-SCAFFOLDED a deliberately deleted file. "+
				"Deleting is an act of ownership and the file's own banner promises "+
				"forge will not write it again; 'scaffold-once' must not mean "+
				"'restore if absent'", i)
		}
		if _, statErr := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(statErr) {
			t.Fatalf("regenerate #%d: file is back on disk (stat err = %v)", i, statErr)
		}
	}
}

// TestScaffoldOnce_FirstScaffoldStillWrites is the sibling that keeps the
// fix honest. Making deletion stick is trivial if you simply stop writing;
// a genuinely new project has NO ledger entry and must still be born.
func TestScaffoldOnce_FirstScaffoldStillWrites(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/handlers/storefront/handlers_crud_test.go"
	body := []byte("package storefront // lifecycle test\n")

	if recorded := ScaffoldRecorded(root, rel); recorded {
		t.Fatal("a fresh project must have NO birth record for this path; " +
			"the ledger is reporting a scaffold that never happened")
	}
	wrote, err := WriteScaffoldIfMissing(root, rel, body)
	if err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if !wrote {
		t.Fatal("first scaffold must write into a project that never had this file")
	}
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read born scaffold: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("born scaffold content = %q, want %q", got, body)
	}
	if !ScaffoldRecorded(root, rel) {
		t.Fatal("a completed birth must leave a ledger record, else the NEXT " +
			"deletion is indistinguishable from 'never scaffolded' and gets undone")
	}
}

// TestScaffoldOnce_ExistingFileIsNeverOverwritten pins the property that
// already worked, so the ledger cannot regress it.
func TestScaffoldOnce_ExistingFileIsNeverOverwritten(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/app/auth.go"
	if _, err := WriteScaffoldIfMissing(root, rel, []byte("package app // born\n")); err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	edited := []byte("package app // MY edits\n")
	if err := os.WriteFile(filepath.Join(root, rel), edited, 0o644); err != nil {
		t.Fatalf("hand-edit: %v", err)
	}
	wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package app // born\n"))
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if wrote {
		t.Fatal("an existing scaffold must never be rewritten")
	}
	got, _ := os.ReadFile(filepath.Join(root, rel))
	if string(got) != string(edited) {
		t.Fatalf("hand-edits were clobbered: got %q, want %q", got, edited)
	}
}

// TestScaffoldOnce_LegacyProjectAdoptsExistingFile covers the upgrade path.
// A project scaffolded by an OLDER forge has the file on disk but no ledger
// entry. Without backfill, its owner's first deletion would look like
// "never scaffolded" and be undone exactly once more — the defect surviving
// one extra round for every existing project.
func TestScaffoldOnce_LegacyProjectAdoptsExistingFile(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/app/compose.go"
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Pre-existing file, no ledger entry: the legacy state.
	if err := os.WriteFile(full, []byte("package app // from an older forge\n"), 0o644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	if ScaffoldRecorded(root, rel) {
		t.Fatal("test premise broken: the legacy state has no ledger entry")
	}

	// A generate run sees it and must adopt it.
	if wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package app // fresh\n")); err != nil || wrote {
		t.Fatalf("legacy file must not be rewritten (wrote=%v, err=%v)", wrote, err)
	}
	if !ScaffoldRecorded(root, rel) {
		t.Fatal("an existing scaffold-once file must be ADOPTED into the ledger, " +
			"otherwise the owner's first deletion is still silently undone")
	}

	// Now the owner deletes it. It must stay deleted on the very first try.
	if err := os.Remove(full); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package app // fresh\n")); err != nil || wrote {
		t.Fatalf("deletion in a legacy project must stick on the FIRST regenerate "+
			"(wrote=%v, err=%v)", wrote, err)
	}
}

// TestScaffoldOnce_LedgerEntryRemovalReScaffolds pins the deliberate reset.
// Deleting the file is no longer the way to get a fresh copy, so there has
// to BE another way — dropping the ledger entry. If this fails, the fix has
// made scaffolds unrecoverable rather than merely stable.
func TestScaffoldOnce_LedgerEntryRemovalReScaffolds(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/handlers/storefront/handlers_crud_test.go"
	body := []byte("package storefront\n")

	if _, err := WriteScaffoldIfMissing(root, rel, body); err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Confirm it is suppressed...
	if wrote, _ := WriteScaffoldIfMissing(root, rel, body); wrote {
		t.Fatal("premise broken: deletion should be sticking by now")
	}
	// ...then take the documented reset and confirm it comes back.
	ForgetScaffold(root, rel)
	wrote, err := WriteScaffoldIfMissing(root, rel, body)
	if err != nil {
		t.Fatalf("re-scaffold after ledger reset: %v", err)
	}
	if !wrote {
		t.Fatal("dropping the ledger entry is the documented way to re-scaffold; " +
			"if it does not write, a deleted scaffold is unrecoverable")
	}
}

// TestScaffoldOnce_LedgerPersistsAcrossProcesses pins that the record is on
// DISK, not merely in the in-memory cache. A ledger that lived only in
// process memory would suppress re-scaffolding within one `forge generate`
// and then hand the file back on the next invocation — precisely the
// observed 15:44 / 15:48 / 15:49 pattern, which spanned three processes.
func TestScaffoldOnce_LedgerPersistsAcrossProcesses(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/app/auth.go"
	body := []byte("package app\n")

	if _, err := WriteScaffoldIfMissing(root, rel, body); err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Simulate a brand-new forge process: nothing cached in memory.
	ResetScaffoldLedgerCache()

	if wrote, err := WriteScaffoldIfMissing(root, rel, body); err != nil || wrote {
		t.Fatalf("deletion must survive a fresh process — the ledger has to be "+
			"read back from %s (wrote=%v, err=%v)", ScaffoldedDir, wrote, err)
	}
	// And the on-disk ledger must actually name the path.
	paths := ScaffoldLedgerPaths(root)
	if len(paths) == 0 {
		t.Fatalf("derived ledger path set is EMPTY — the ledger at %s recorded "+
			"nothing, so every assertion above is vacuous", ScaffoldedDir)
	}
	found := false
	for _, p := range paths {
		if p == rel {
			found = true
		}
	}
	if !found {
		t.Fatalf("ledger paths %v do not include %q", paths, rel)
	}
}

// TestScaffoldOnce_FailedRunForgetsBirth covers the interaction with the
// rollback journal. A failed `forge generate` deletes the scaffolds it
// created this run; if the birth record survived that deletion, the failed
// run would have consumed the path's ONE chance to be born and no later run
// would ever emit it.
func TestScaffoldOnce_FailedRunForgetsBirth(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/app/auth.go"
	body := []byte("package app\n")

	BeginRollbackJournal(root)
	wrote, err := WriteScaffoldIfMissing(root, rel, body)
	if err != nil || !wrote {
		t.Fatalf("scaffold under an armed journal must write (wrote=%v, err=%v)", wrote, err)
	}
	if !ScaffoldRecorded(root, rel) {
		t.Fatal("premise broken: the birth should be recorded before the rollback")
	}

	// The run fails its validate step and rewinds.
	restored := RestoreRollback(root)
	if len(restored) == 0 {
		t.Fatal("derived rollback set is EMPTY — the journal captured nothing, " +
			"so this test proves nothing about the rollback interaction")
	}
	if _, statErr := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(statErr) {
		t.Fatalf("rollback must delete the scaffold it created (stat err = %v)", statErr)
	}
	if ScaffoldRecorded(root, rel) {
		t.Fatal("a rolled-back birth must be FORGOTTEN: the file is gone, so a " +
			"surviving record would permanently suppress a scaffold that never shipped")
	}

	// Proof it is genuinely recoverable: the next run scaffolds it.
	ResetScaffoldLedgerCache()
	if wrote, err := WriteScaffoldIfMissing(root, rel, body); err != nil || !wrote {
		t.Fatalf("after a rolled-back run the next generate must scaffold "+
			"(wrote=%v, err=%v)", wrote, err)
	}
}

// TestScaffoldOnce_FailedRunRestoresLedgerBytes pins the FILE, not only the
// answer ScaffoldRecorded gives. generate's rollback verdict compares the
// tree — .forge/scaffolded/ included — against its pre-run fingerprint,
// so a failed run that leaves the ledger rewritten cannot report the tree
// back in its pre-run state, even when every birth it recorded was dropped.
func TestScaffoldOnce_FailedRunRestoresLedgerBytes(t *testing.T) {
	root := newScaffoldProject(t)
	const (
		earlier = "internal/app/auth.go"
		thisRun = "internal/app/login_broker.go"
	)
	if wrote, err := WriteScaffoldIfMissing(root, earlier, []byte("package app\n")); err != nil || !wrote {
		t.Fatalf("seed an earlier birth (wrote=%v, err=%v)", wrote, err)
	}
	before := ledgerFileBytes(t, root)
	if len(before) == 0 {
		t.Fatalf("premise broken: the earlier birth left no ledger to restore")
	}
	ResetScaffoldLedgerCache() // the failing generate is a fresh process

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	if wrote, err := WriteScaffoldIfMissing(root, thisRun, []byte("package app\n")); err != nil || !wrote {
		t.Fatalf("scaffold under an armed journal must write (wrote=%v, err=%v)", wrote, err)
	}
	if during := ledgerFileBytes(t, root); bytes.Equal(during, before) {
		t.Fatal("premise broken: this run's birth should have rewritten the ledger")
	}

	RestoreRollback(root)

	after := ledgerFileBytes(t, root)
	if !bytes.Equal(after, before) {
		t.Errorf("a rolled-back run must leave %s byte-for-byte as it found it\nbefore:\n%s\nafter:\n%s",
			ScaffoldedDir, before, after)
	}
	if ScaffoldRecorded(root, thisRun) {
		t.Error("the rolled-back birth must be forgotten, in memory as well as on disk")
	}
	if !ScaffoldRecorded(root, earlier) {
		t.Error("a birth recorded before the run must survive its rollback")
	}
}

// TestScaffoldOnce_SteadyStateWritesNoLedgerChurn guards the reason this
// package abandoned the global manifest: bookkeeping that rewrites on every
// run turns real commits into diff noise. Re-recording a known path must
// not touch the file.
func TestScaffoldOnce_SteadyStateWritesNoLedgerChurn(t *testing.T) {
	root := newScaffoldProject(t)
	const rel = "internal/app/auth.go"
	if _, err := WriteScaffoldIfMissing(root, rel, []byte("package app\n")); err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	before := ledgerFileBytes(t, root)
	if len(before) == 0 {
		t.Fatal("ledger is empty after a birth — nothing to test for churn")
	}
	// Ten idempotent re-runs, the steady state.
	for i := 0; i < 10; i++ {
		if _, err := WriteScaffoldIfMissing(root, rel, []byte("package app\n")); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	after := ledgerFileBytes(t, root)
	if string(before) != string(after) {
		t.Fatalf("steady-state re-runs churned the ledger:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestAbsentScaffolds_ReportsTheDeletedOnesOnly is the query behind the
// generate-time warning. The ledger's suppression of a deleted scaffold is
// correct and stays; what was missing is any way for forge to SAY it is
// suppressing something. A run deleted its CRUD lifecycle test expecting
// generate to re-derive it, generate reported success in silence, and the
// file was gone.
//
// The two arms matter equally. Reporting the deleted path is the feature;
// NOT reporting the present one is what keeps the report worth reading —
// a warning that fires for every scaffold in a healthy project is noise
// users learn to skip, which is how the real signal gets missed.
func TestAbsentScaffolds_ReportsTheDeletedOnesOnly(t *testing.T) {
	root := newScaffoldProject(t)
	const (
		deleted = "internal/handlers/library/handlers_crud_test.go"
		kept    = "internal/app/auth.go"
	)
	for _, rel := range []string{deleted, kept} {
		if wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package x\n")); err != nil || !wrote {
			t.Fatalf("seed scaffold %s (wrote=%v, err=%v)", rel, wrote, err)
		}
	}
	// The author hardens their migrations, the born fixtures no longer
	// satisfy them, and they delete the test expecting a re-derivation.
	if err := os.Remove(filepath.Join(root, deleted)); err != nil {
		t.Fatalf("delete %s: %v", deleted, err)
	}

	got := AbsentScaffolds(root)
	if len(got) != 1 || got[0] != deleted {
		t.Fatalf("AbsentScaffolds = %v, want exactly [%s].\n"+
			"A scaffold-once path recorded in the birth ledger but absent on disk is a "+
			"file forge is deliberately declining to write. That decision is right, but it "+
			"has to be VISIBLE: the run this pins deleted the file expecting generate to "+
			"re-derive it, generate said nothing, and an hour went into trying to "+
			"reproduce the birth condition by hand.", got, deleted)
	}
}

// TestAbsentScaffolds_SilentOnAHealthyProject is the noise guard stated
// on its own. Every scaffold present ⇒ nothing to report, so a steady-state
// `forge generate` prints no scaffold warning at all.
func TestAbsentScaffolds_SilentOnAHealthyProject(t *testing.T) {
	root := newScaffoldProject(t)
	for _, rel := range []string{"internal/app/auth.go", "cmd/app/main.go"} {
		if wrote, err := WriteScaffoldIfMissing(root, rel, []byte("package x\n")); err != nil || !wrote {
			t.Fatalf("seed scaffold %s (wrote=%v, err=%v)", rel, wrote, err)
		}
	}
	if got := AbsentScaffolds(root); len(got) != 0 {
		t.Fatalf("AbsentScaffolds = %v on a project where every scaffold is present; "+
			"want none. A warning that fires in the healthy case trains users to ignore "+
			"the one that matters", got)
	}
}
