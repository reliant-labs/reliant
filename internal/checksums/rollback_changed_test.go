// Tests for ChangedJournalPaths: which of the paths a restore owns did the
// run actually change? The post-rollback compile check runs only when one of
// them is a Go build input, so a path reported here that did not change costs
// a full build, and a changed path left out skips a build that was owed.
package checksums

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChangedJournalPaths_OnlyWhatTheRunChanged(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "forge.yaml", "name: app\n")
	seedFile(t, root, "internal/app/same.go", "package app\n")
	seedFile(t, root, "internal/app/edited.go", "package app // before\n")
	seedFile(t, root, "internal/app/deleted.go", "package app\n")
	seedFile(t, root, "gen/a/v1/a.pb.go", "package a\n")

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "gen")

	// Rewritten with identical bytes: journaled, but not a change.
	RecordPreWrite(root, "internal/app/same.go")
	seedFile(t, root, "internal/app/same.go", "package app\n")
	// Modified, deleted, created.
	RecordPreWrite(root, "forge.yaml")
	seedFile(t, root, "forge.yaml", "name: app\nkind: service\n")
	RecordPreWrite(root, "internal/app/edited.go")
	seedFile(t, root, "internal/app/edited.go", "package app // after\n")
	if err := RemoveJournaled(filepath.Join(root, "internal/app/deleted.go")); err != nil {
		t.Fatal(err)
	}
	RecordPreWrite(root, "internal/app/new.go")
	seedFile(t, root, "internal/app/new.go", "package app\n")
	// An external tool created a file under its captured tree, and left the
	// file that was already there alone.
	seedFile(t, root, "gen/b/v1/b.pb.go", "package b\n")

	got := ChangedJournalPaths(root)
	want := []string{
		"forge.yaml",
		"gen/b/v1/b.pb.go",
		"internal/app/deleted.go",
		"internal/app/edited.go",
		"internal/app/new.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedJournalPaths = %v, want %v", got, want)
	}
}

func TestChangedJournalPaths_JournalOffIsEmpty(t *testing.T) {
	CommitRollback()
	if got := ChangedJournalPaths(t.TempDir()); len(got) != 0 {
		t.Errorf("no journal armed, got %v", got)
	}
}

// A path captured as absent that the run turned into a DIRECTORY is not a
// changed file of its own; what the tool wrote inside it is.
func TestChangedJournalPaths_AbsentPathBecameDirectory(t *testing.T) {
	root := t.TempDir()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)
	CaptureExternalWrites(root, "openapi")
	if err := os.MkdirAll(filepath.Join(root, "openapi"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedFile(t, root, "openapi/svc.yaml", "openapi: 3.1.0\n")

	got := ChangedJournalPaths(root)
	if want := []string{"openapi/svc.yaml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedJournalPaths = %v, want %v", got, want)
	}
}
