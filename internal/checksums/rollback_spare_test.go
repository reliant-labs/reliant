// Tests for the rollback's other-writer guard (forge#2267).
//
// Contract pinned here: a failed run rewinds a journaled path only while it
// is still what this run's writers left there. A path another writer
// changed after forge's write — an agent editing a hand-owned file in the
// same checkout — is spared and named, never reverted; and a file under a
// captured directory is deleted only when it is the one the run created.
package checksums

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func readFileT(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// skipUnlessModeDeniesReads skips a test that builds an unreadable file with
// chmod 000. Root reads such a file anyway, and on Windows permission bits
// only toggle the read-only attribute, so the file stays readable. The guarded
// behaviour is platform-neutral: observe and the capture treat any read error
// (a Windows ACL denial or sharing violation included) as unreadable.
func skipUnlessModeDeniesReads(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not make a file unreadable on Windows; the unreadable case cannot be built with chmod")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file; the unreadable case cannot be built")
	}
}

// The observed failure: forge rewrote a hand-owned file early in the run,
// another agent then added helpers to it, and the failed run's rewind put
// back the pre-run bytes and erased them.
func TestRestore_SparesAPathAnotherWriterChangedAfterForge(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "internal/reclaim/restore.go", "package reclaim // pre-run\n")
	seedFile(t, root, "internal/db/models_gen.go", "package db // pre-run\n")

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// Forge's writes this run: one through the chokepoint's precise note,
	// one recorded at the step's settle point.
	recordPreWrite(root, "internal/reclaim/restore.go")
	forgeBytes := []byte("package reclaim // forge's rewrite\n")
	seedFile(t, root, "internal/reclaim/restore.go", string(forgeBytes))
	notePostWrite("internal/reclaim/restore.go", forgeBytes)
	recordPreWrite(root, "internal/db/models_gen.go")
	seedFile(t, root, "internal/db/models_gen.go", "package db // forge's render\n")
	SettleJournal(root)

	// Another agent edits the hand-owned file after forge wrote it.
	const theirs = "package reclaim // forge's rewrite\n\nfunc newHelper() {}\n"
	seedFile(t, root, "internal/reclaim/restore.go", theirs)

	res := RestoreRollbackDetailed(root)
	if !reflect.DeepEqual(res.Spared, []string{"internal/reclaim/restore.go"}) {
		t.Errorf("spared = %v, want the file another writer changed", res.Spared)
	}
	if !reflect.DeepEqual(res.Restored, []string{"internal/db/models_gen.go"}) || len(res.Failed) != 0 {
		t.Errorf("restored = %v, failed = %v; want only forge's own output rewound", res.Restored, res.Failed)
	}
	if got := readFileT(t, root, "internal/reclaim/restore.go"); got != theirs {
		t.Errorf("the other writer's bytes were not kept:\n%s", got)
	}
	if got := readFileT(t, root, "internal/db/models_gen.go"); got != "package db // pre-run\n" {
		t.Errorf("forge's output was not rewound: %q", got)
	}
}

// RestoreRollbackReport keeps its contract: a spared path is one it could
// not restore, so no caller claims a restore that did not happen.
func TestRestoreRollbackReport_CountsSparedAsNotRestored(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.go", "package a // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	recordPreWrite(root, "a.go")
	seedFile(t, root, "a.go", "package a // forge\n")
	SettleJournal(root)
	seedFile(t, root, "a.go", "package a // theirs\n")

	restored, failed := RestoreRollbackReport(root)
	if len(restored) != 0 || !reflect.DeepEqual(failed, []string{"a.go"}) {
		t.Errorf("restored = %v, failed = %v; want a.go reported as not restored", restored, failed)
	}
}

// A path the other writer put back to its pre-run bytes, or deleted when
// forge had created it, is already where the rewind would put it: restored,
// not spared.
func TestRestore_APathAlreadyAtItsPreRunStateIsNotSpared(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.go", "package a // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	recordPreWrite(root, "a.go")
	recordPreWrite(root, "born.go")
	seedFile(t, root, "a.go", "package a // forge\n")
	seedFile(t, root, "born.go", "package a // forge created me\n")
	SettleJournal(root)
	seedFile(t, root, "a.go", "package a // pre-run\n")
	if err := os.Remove(filepath.Join(root, "born.go")); err != nil {
		t.Fatal(err)
	}

	res := RestoreRollbackDetailed(root)
	if len(res.Spared) != 0 || !reflect.DeepEqual(res.Restored, []string{"a.go", "born.go"}) {
		t.Errorf("restored = %v, spared = %v; want both restored", res.Restored, res.Spared)
	}
}

// Under a directory an external tool writes into, the rewind deletes the
// files the tool created this run and spares one another writer created or
// changed there afterwards.
func TestRestore_SparesFilesAnotherWriterLeftUnderACapturedTree(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/a/v1/a.pb.go", "package a // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	CaptureExternalWrites(root, "gen")
	// The tool's step: rewrite one stub, create two.
	seedFile(t, root, "gen/a/v1/a.pb.go", "package a // tool\n")
	seedFile(t, root, "gen/b/v1/b.pb.go", "package b // tool\n")
	seedFile(t, root, "gen/c/v1/c.pb.go", "package c // tool\n")
	SettleJournal(root)

	// Another agent's own buf run, after the step: a new stub, and an edit
	// to one the tool created.
	seedFile(t, root, "gen/d/v1/d.pb.go", "package d // theirs\n")
	seedFile(t, root, "gen/c/v1/c.pb.go", "package c // theirs\n")

	res := RestoreRollbackDetailed(root)
	if want := []string{"gen/c/v1/c.pb.go", "gen/d/v1/d.pb.go"}; !reflect.DeepEqual(res.Spared, want) {
		t.Errorf("spared = %v, want %v", res.Spared, want)
	}
	if want := []string{"gen/a/v1/a.pb.go", "gen/b/v1/b.pb.go"}; !reflect.DeepEqual(res.Restored, want) {
		t.Errorf("restored = %v, want %v", res.Restored, want)
	}
	if _, err := os.Stat(filepath.Join(root, "gen/b/v1/b.pb.go")); !os.IsNotExist(err) {
		t.Error("the stub the tool created this run was not deleted")
	}
	for rel, want := range map[string]string{
		"gen/a/v1/a.pb.go": "package a // pre-run\n",
		"gen/c/v1/c.pb.go": "package c // theirs\n",
		"gen/d/v1/d.pb.go": "package d // theirs\n",
	} {
		if got := readFileT(t, root, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// A caller that never settles (forge project upgrade's module step) keeps
// the old behaviour: everything written since the journal began is forge's,
// and is rewound.
func TestRestore_WithoutSettlePointsRewindsEverything(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "go.mod", "module x // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "go.mod")
	seedFile(t, root, "go.mod", "module x // tidied\n")

	res := RestoreRollbackDetailed(root)
	if len(res.Spared) != 0 || !reflect.DeepEqual(res.Restored, []string{"go.mod"}) {
		t.Errorf("restored = %v, spared = %v; want go.mod rewound", res.Restored, res.Spared)
	}
	if got := readFileT(t, root, "go.mod"); got != "module x // pre-run\n" {
		t.Errorf("go.mod = %q", got)
	}
}

// Review on forge#2329: the rewind fails closed. A path it cannot read is
// left as is and reported unverified — never overwritten, and never deleted
// as though it were absent.
func TestRestore_LeavesAnUnreadablePathAsIs(t *testing.T) {
	skipUnlessModeDeniesReads(t)
	root := t.TempDir()
	seedFile(t, root, "a.go", "package a // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	recordPreWrite(root, "a.go")
	recordPreWrite(root, "born.go")
	seedFile(t, root, "a.go", "package a // forge\n")
	seedFile(t, root, "born.go", "package a // forge created me\n")
	SettleJournal(root)
	// Another writer's files, which the rewind cannot read.
	seedFile(t, root, "a.go", "package a // theirs\n")
	seedFile(t, root, "born.go", "package a // theirs too\n")
	for _, rel := range []string{"a.go", "born.go"} {
		full := filepath.Join(root, rel)
		if err := os.Chmod(full, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
	}

	res := RestoreRollbackDetailed(root)
	if want := []string{"a.go", "born.go"}; !reflect.DeepEqual(res.Unverified, want) || len(res.Restored) != 0 {
		t.Errorf("unverified = %v, restored = %v; want both left as is and reported", res.Unverified, res.Restored)
	}
	for rel, want := range map[string]string{"a.go": "package a // theirs\n", "born.go": "package a // theirs too\n"} {
		full := filepath.Join(root, rel)
		_ = os.Chmod(full, 0o644)
		if got := readFileT(t, root, rel); got != want {
			t.Errorf("%s = %q, want the other writer's bytes untouched", rel, got)
		}
	}
}

// A pre-run file the capture cannot read is never treated as absent (the
// rewind would delete it): it is unverifiable, and left as is.
func TestRestore_UnreadableBeforeTheRunIsNotTreatedAsAbsent(t *testing.T) {
	skipUnlessModeDeniesReads(t)
	root := t.TempDir()
	seedFile(t, root, "secret.go", "package a // pre-run\n")
	full := filepath.Join(root, "secret.go")
	if err := os.Chmod(full, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	recordPreWrite(root, "secret.go")

	restored, failed := RestoreRollbackReport(root)
	if len(restored) != 0 || !reflect.DeepEqual(failed, []string{"secret.go"}) {
		t.Errorf("restored = %v, failed = %v; want secret.go reported, not restored", restored, failed)
	}
	if _, err := os.Lstat(full); err != nil {
		t.Errorf("the unreadable pre-run file was removed: %v", err)
	}
}

// A journaled file another writer replaced with a symlink, or with a
// directory, is a change: spared and reported, the link never followed or
// written through, and nothing removed.
func TestRestore_SparesAFileReplacedByASymlinkOrADirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(target, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "linked.go", "package a // pre-run\n")
	seedFile(t, root, "dir.go", "package a // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	for _, rel := range []string{"linked.go", "dir.go", "born_link.go"} {
		recordPreWrite(root, rel)
		seedFile(t, root, rel, "package a // forge\n")
	}
	SettleJournal(root)

	// Another writer: two paths become symlinks (one forge created), one a
	// directory with a file of theirs in it.
	for _, rel := range []string{"linked.go", "born_link.go"} {
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, rel)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("creating a symlink needs Developer Mode or SeCreateSymbolicLinkPrivilege on Windows: %v", err)
			}
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "dir.go")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "dir.go/theirs.txt", "theirs\n")

	res := RestoreRollbackDetailed(root)
	if want := []string{"born_link.go", "dir.go", "linked.go"}; !reflect.DeepEqual(res.Spared, want) {
		t.Errorf("spared = %v, want %v", res.Spared, want)
	}
	if len(res.Restored) != 0 || len(res.Failed) != 0 {
		t.Errorf("restored = %v, failed = %v; want nothing rewound", res.Restored, res.Failed)
	}
	for _, rel := range []string{"linked.go", "born_link.go"} {
		info, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is no longer the other writer's symlink: %v %v", rel, info, err)
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "package outside\n" {
		t.Errorf("the rewind wrote through the symlink: %q", got)
	}
	if got := readFileT(t, root, "dir.go/theirs.txt"); got != "theirs\n" {
		t.Errorf("the directory's content was touched: %q", got)
	}
}

// symlinkOrSkip makes link point at target, skipping where the host cannot
// create a symlink (Windows without Developer Mode).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symlink needs Developer Mode or SeCreateSymbolicLinkPrivilege on Windows: %v", err)
		}
		t.Fatal(err)
	}
}

// Review on forge#2329 (R2.B1): a captured tree another writer replaced
// with a symlink to a directory is never walked. Files there that hold the
// very bytes this run's tool wrote are the other writer's, and survive.
func TestRestore_NeverWalksATreeReplacedByASymlink(t *testing.T) {
	root := t.TempDir()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "gen")
	seedFile(t, root, "gen/out.go", "package gen // tool\n")
	SettleJournal(root)

	// Another writer moves the tool's output aside and links gen to it.
	elsewhere := filepath.Join(t.TempDir(), "kept")
	if err := os.Rename(filepath.Join(root, "gen"), elsewhere); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, elsewhere, filepath.Join(root, "gen"))

	res := RestoreRollbackDetailed(root)
	if !reflect.DeepEqual(res.Spared, []string{"gen"}) || len(res.Restored) != 0 {
		t.Errorf("spared = %v, restored = %v; want gen spared, nothing removed", res.Spared, res.Restored)
	}
	if got, err := os.ReadFile(filepath.Join(elsewhere, "out.go")); err != nil || string(got) != "package gen // tool\n" {
		t.Errorf("a file beneath the symlinked tree was touched: %q %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "gen")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("gen is no longer the other writer's symlink: %v %v", info, err)
	}
}

// A captured tree another writer moved aside and re-created as a new
// directory is theirs: neither the files created there nor the journaled
// ones beneath it are rewound. (The old directory is kept, so the new one
// cannot reuse its inode.)
func TestRestore_NeverWalksATreeReplacedByAnotherDirectory(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/old.go", "package gen // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "gen")
	seedFile(t, root, "gen/old.go", "package gen // tool\n")
	seedFile(t, root, "gen/new.go", "package gen // tool\n")
	SettleJournal(root)

	if err := os.Rename(filepath.Join(root, "gen"), filepath.Join(root, "gen.bak")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "gen/old.go", "package gen // tool\n")
	seedFile(t, root, "gen/new.go", "package gen // tool\n")

	res := RestoreRollbackDetailed(root)
	if want := []string{"gen", "gen/old.go"}; !reflect.DeepEqual(res.Spared, want) || len(res.Restored) != 0 {
		t.Errorf("spared = %v, restored = %v; want %v spared, nothing rewound", res.Spared, res.Restored, want)
	}
	for _, rel := range []string{"gen/old.go", "gen/new.go"} {
		if got := readFileT(t, root, rel); got != "package gen // tool\n" {
			t.Errorf("%s = %q, want the other writer's file untouched", rel, got)
		}
	}
}

// A tool that regenerates its declared directory atomically — into scratch,
// then renamed over the live one, as reliant's sdk-go-gen hook does — swaps
// in a new directory during its own step. That directory is the tool's
// output, not another writer's: a failed run rewinds it like files written
// in place (forge bug 9ceafeb4: sdk/go/gen stayed modified after a failed
// validate).
func TestRestore_RewindsATreeTheToolSwappedInDuringItsStep(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/old.go", "package gen // pre-run\n")
	seedFile(t, root, "gen/gone.go", "package gen // pre-run, not regenerated\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "gen")

	// The tool's step: generate into scratch, move the live tree aside,
	// rename the scratch tree into place, drop the old one.
	seedFile(t, root, "tmp/stage/gen/old.go", "package gen // tool\n")
	seedFile(t, root, "tmp/stage/gen/new.go", "package gen // tool\n")
	if err := os.Rename(filepath.Join(root, "gen"), filepath.Join(root, "tmp/stage/previous")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "tmp/stage/gen"), filepath.Join(root, "gen")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "tmp")); err != nil {
		t.Fatal(err)
	}
	SettleJournal(root)

	res := RestoreRollbackDetailed(root)
	if len(res.Spared) != 0 || len(res.Unverified) != 0 {
		t.Errorf("spared = %v, unverified = %v; want the tool's own swap rewound", res.Spared, res.Unverified)
	}
	if got := readFileT(t, root, "gen/old.go"); got != "package gen // pre-run\n" {
		t.Errorf("gen/old.go = %q, want its pre-run bytes", got)
	}
	if got := readFileT(t, root, "gen/gone.go"); got != "package gen // pre-run, not regenerated\n" {
		t.Errorf("gen/gone.go = %q, want it recreated", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "gen/new.go")); !os.IsNotExist(err) {
		t.Errorf("gen/new.go, created by the failed run, survived the rewind: %v", err)
	}
}

// An external path absent before the run that another writer made a
// symlink to a directory is not settled: it is spared, not followed.
func TestRestore_AnAbsentExternalPathMadeASymlinkIsSpared(t *testing.T) {
	root := t.TempDir()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "openapi")
	SettleJournal(root)
	dir := t.TempDir()
	seedFile(t, dir, "spec.json", "{}\n")
	symlinkOrSkip(t, dir, filepath.Join(root, "openapi"))

	res := RestoreRollbackDetailed(root)
	if !reflect.DeepEqual(res.Spared, []string{"openapi"}) || len(res.Restored) != 0 {
		t.Errorf("spared = %v, restored = %v; want openapi spared", res.Spared, res.Restored)
	}
	if got := readFileT(t, dir, "spec.json"); got != "{}\n" {
		t.Errorf("the symlink's target was touched: %q", got)
	}
}

// Review on forge#2329 (B1): a path whose parent another writer removed after
// forge's step is spared, never recreated with its parent; one whose parent
// is now a file is spared too.
func TestRestore_NeverRecreatesAParentAnotherWriterRemoved(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "pkg/gone/old.go", "package gone // pre-run\n")
	seedFile(t, root, "pkg/gone/keep.go", "package gone\n")
	seedFile(t, root, "pkg/file/old.go", "package file // pre-run\n")
	seedFile(t, root, "pkg/file/keep.go", "package file\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// Forge's step removes each old.go; its directory stays (keep.go).
	for _, rel := range []string{"pkg/gone/old.go", "pkg/file/old.go"} {
		recordPreWrite(root, rel)
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	SettleJournal(root)

	// Another writer then deletes one directory and puts a file where the
	// other was.
	if err := os.RemoveAll(filepath.Join(root, "pkg/gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "pkg/file")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "pkg/file", "not a directory\n")

	res := RestoreRollbackDetailed(root)
	if want := []string{"pkg/file/old.go", "pkg/gone/old.go"}; !reflect.DeepEqual(res.Spared, want) {
		t.Errorf("spared = %v, want %v", res.Spared, want)
	}
	if len(res.Restored) != 0 || len(res.Failed) != 0 {
		t.Errorf("restored = %v, failed = %v; want nothing rewritten", res.Restored, res.Failed)
	}
	if _, err := os.Lstat(filepath.Join(root, "pkg/gone")); !os.IsNotExist(err) {
		t.Errorf("the removed parent was recreated (err = %v)", err)
	}
	if got := readFileT(t, root, "pkg/file"); got != "not a directory\n" {
		t.Errorf("the file in the parent's place was touched: %q", got)
	}
}

// A parent forge's own step removed (it pruned the directory with the file)
// is recreated: the rewind puts back what forge took away.
func TestRestore_RecreatesAParentForgeRemoved(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "pkg/only/old.go", "package only // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	recordPreWrite(root, "pkg/only/old.go")
	if err := os.RemoveAll(filepath.Join(root, "pkg/only")); err != nil {
		t.Fatal(err)
	}
	SettleJournal(root)

	res := RestoreRollbackDetailed(root)
	if len(res.Spared) != 0 || !reflect.DeepEqual(res.Restored, []string{"pkg/only/old.go"}) {
		t.Errorf("restored = %v, spared = %v; want old.go restored", res.Restored, res.Spared)
	}
	if got := readFileT(t, root, "pkg/only/old.go"); got != "package only // pre-run\n" {
		t.Errorf("old.go = %q", got)
	}
}

// The rewind's own pruning never makes a later path look as though another
// writer removed its parent: forge deleted old.go and created new.go beside
// it, so rewinding new.go empties the directory old.go goes back into.
func TestRestore_ItsOwnPruneDoesNotSpareASibling(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "pkg/swap/old.go", "package swap // pre-run\n")
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	recordPreWrite(root, "pkg/swap/old.go")
	if err := os.Remove(filepath.Join(root, "pkg/swap/old.go")); err != nil {
		t.Fatal(err)
	}
	recordPreWrite(root, "pkg/swap/new.go")
	seedFile(t, root, "pkg/swap/new.go", "package swap // forge\n")
	SettleJournal(root)

	res := RestoreRollbackDetailed(root)
	if len(res.Spared) != 0 || !reflect.DeepEqual(res.Restored, []string{"pkg/swap/new.go", "pkg/swap/old.go"}) {
		t.Errorf("restored = %v, spared = %v; want both rewound", res.Restored, res.Spared)
	}
	if got := readFileT(t, root, "pkg/swap/old.go"); got != "package swap // pre-run\n" {
		t.Errorf("old.go = %q", got)
	}
}
