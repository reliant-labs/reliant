// Tests for the external-tool side of the rollback journal
// (CaptureExternalWrites).
//
// Contract pinned here: a path declared as an external tool's output is
// restored exactly like a forge write — modified files get their pre-run
// bytes back, files the tool created are deleted, files it deleted come back
// — while the paths the tool left alone are neither rewritten, listed as
// restored, nor counted as writes in the ledger.
package checksums

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCaptureExternalWrites_RestoresToolOutput(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/a/v1/a.pb.go", "// protoc-gen-go v1.36.12\npackage a\n")
	seedFile(t, root, "gen/b/v1/b.pb.go", "package b\n")
	seedFile(t, root, "gen/gone/v1/gone.pb.go", "package gone\n")
	seedFile(t, root, "go.sum", "pre-run sum\n")

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	// "openapi" and "gen/go.sum" do not exist yet.
	CaptureExternalWrites(root, "gen", "go.sum", "openapi", "gen/go.sum")

	// The tool run: rewrite one stub, leave one alone, delete one, create
	// files in an existing and in a brand-new directory.
	seedFile(t, root, "gen/a/v1/a.pb.go", "// protoc-gen-go v1.36.11\npackage a\n")
	if err := os.Remove(filepath.Join(root, "gen/gone/v1/gone.pb.go")); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "gen/new/v1/new.pb.go", "package new\n")
	seedFile(t, root, "openapi/svc.yaml", "openapi: 3.1.0\n")
	seedFile(t, root, "go.sum", "tidied sum\n")
	seedFile(t, root, "gen/go.sum", "new sum\n")

	sum := SummarizeWrites(root)
	if sum.Touched != 6 {
		t.Errorf("Touched = %d, want 6 (the unchanged b.pb.go is not a write; new files outside the journal are not counted): %+v", sum.Touched, sum)
	}

	restored, failed := RestoreRollbackReport(root)
	if len(failed) != 0 {
		t.Fatalf("restore failed for %v", failed)
	}
	want := []string{
		"gen/a/v1/a.pb.go",
		"gen/go.sum",
		"gen/gone/v1/gone.pb.go",
		"gen/new/v1/new.pb.go",
		"go.sum",
		"openapi/svc.yaml",
	}
	if !reflect.DeepEqual(restored, want) {
		t.Errorf("restored = %v\nwant       %v", restored, want)
	}

	assertBody(t, root, "gen/a/v1/a.pb.go", "// protoc-gen-go v1.36.12\npackage a\n")
	assertBody(t, root, "gen/b/v1/b.pb.go", "package b\n")
	assertBody(t, root, "gen/gone/v1/gone.pb.go", "package gone\n")
	assertBody(t, root, "go.sum", "pre-run sum\n")
	for _, gone := range []string{"gen/new/v1/new.pb.go", "gen/new", "openapi", "gen/go.sum"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s was created during the run and must be gone after restore (err=%v)", gone, err)
		}
	}
	if RollbackEnabled() {
		t.Error("restore must turn journaling off")
	}
}

// A path forge itself wrote earlier in the run keeps that capture: the
// external capture must not overwrite the true pre-run bytes with forge's
// own output.
func TestCaptureExternalWrites_FirstCaptureWins(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/go.mod", "module pre-run\n")

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	recordPreWrite(root, "gen/go.mod")
	seedFile(t, root, "gen/go.mod", "module forge-wrote\n")
	CaptureExternalWrites(root, "gen")
	seedFile(t, root, "gen/go.mod", "module tidied\n")

	RestoreRollback(root)
	assertBody(t, root, "gen/go.mod", "module pre-run\n")
}

// A forge write to a path captured ahead of a tool is a write this run made,
// even when the bytes come out identical — the ledger's Touched contract.
func TestCaptureExternalWrites_LaterForgeWriteCounts(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "gen/x.go", "package x\n")

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "gen")
	if got := SummarizeWrites(root).Touched; got != 0 {
		t.Fatalf("an untouched capture is not a write, Touched = %d", got)
	}
	recordPreWrite(root, "gen/x.go")
	if got := SummarizeWrites(root).Touched; got != 1 {
		t.Fatalf("a forge write after the capture must count, Touched = %d", got)
	}
}

func TestCaptureExternalWrites_IgnoresPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "../escape", "/abs/path", ".", "")
	if got := JournaledPaths(); len(got) != 0 {
		t.Fatalf("paths outside the root must not be journaled, got %v", got)
	}
}

func TestCaptureExternalWrites_JournalOffIsNoop(t *testing.T) {
	CommitRollback()
	root := t.TempDir()
	seedFile(t, root, "gen/x.go", "package x\n")
	CaptureExternalWrites(root, "gen")
	if got := JournaledPaths(); len(got) != 0 {
		t.Fatalf("journal is off; got %v", got)
	}
}

func assertBody(t *testing.T, root, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", rel, got, want)
	}
}
