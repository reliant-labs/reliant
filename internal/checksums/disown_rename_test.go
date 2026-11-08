package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// carryFixture sets up a project root with a disowned file at oldRel and
// silences the notice functions, returning the tracker.
func carryFixture(t *testing.T, oldRel, body string) (string, *FileChecksums) {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, filepath.FromSlash(oldRel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	prevNotice, prevConflict := DisownCarryNoticeFn, DisownCarryConflictFn
	DisownCarryNoticeFn = func(string, string) {}
	DisownCarryConflictFn = func(string, string) {}
	ResetWriteBlocks()
	t.Cleanup(func() {
		DisownCarryNoticeFn, DisownCarryConflictFn = prevNotice, prevConflict
		ResetWriteBlocks()
	})
	return root, &FileChecksums{
		Disowned:    map[string]DisownedEntry{oldRel: {Reason: "mine now"}},
		Unstampable: map[string]string{},
	}
}

// The core property: the user's bytes and their disown record both land on
// the new path, and the old path is vacated.
func TestCarryDisownAcrossRename_MovesFileAndRecord(t *testing.T) {
	const body = "package app\n\nfunc Mine() {}\n"
	root, cs := carryFixture(t, "internal/app/mounts_services.go", body)

	if !CarryDisownAcrossRename(root, cs, "internal/app/mounts_services.go", "internal/app/mounts_services_gen.go") {
		t.Fatal("carry-over should have happened")
	}

	if _, err := os.Stat(filepath.Join(root, "internal/app/mounts_services.go")); !os.IsNotExist(err) {
		t.Error("old path should be vacated after the carry-over")
	}
	got, err := os.ReadFile(filepath.Join(root, "internal/app/mounts_services_gen.go"))
	if err != nil {
		t.Fatalf("new path missing: %v", err)
	}
	if string(got) != body {
		t.Errorf("user bytes altered by the move: %q", got)
	}
	if cs.IsDisowned("internal/app/mounts_services.go") {
		t.Error("stale record on the old path must be dropped")
	}
	if !cs.IsDisowned("internal/app/mounts_services_gen.go") {
		t.Error("record must now cover the new path, or the next run re-emits over it")
	}
	// The whole point: a later Tier-1 write to the new path is now skipped.
	wrote, err := WriteGeneratedFile(root, "internal/app/mounts_services_gen.go", []byte("package app\n\nfunc Generated() {}\n"), cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("the emitter must skip the carried path — writing it is the collision")
	}
}

// A file forge did not write, sitting at the new path, is never replaced.
func TestCarryDisownAcrossRename_RefusesOverForeignFile(t *testing.T) {
	root, cs := carryFixture(t, "pkg/config/config.go", "package config\n\n// mine\n")
	const foreign = "package config\n\n// somebody else's\n"
	if err := os.WriteFile(filepath.Join(root, "pkg/config/config_gen.go"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	var conflicts int
	DisownCarryConflictFn = func(string, string) { conflicts++ }

	if !CarryDisownAcrossRename(root, cs, "pkg/config/config.go", "pkg/config/config_gen.go") {
		t.Fatal("a refusal must still report as handled so the caller doesn't delete the old file")
	}
	if conflicts != 1 {
		t.Errorf("conflicts = %d, want 1 loud report", conflicts)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "pkg/config/config_gen.go")); string(got) != foreign {
		t.Error("a file forge did not write must never be replaced")
	}
	if !cs.IsDisowned("pkg/config/config.go") {
		t.Error("the disown must survive a refusal")
	}
	if !WriteBlocked("pkg/config/config_gen.go") {
		t.Error("a refusal must block the write, or the emitter recreates the collision")
	}
}

// Forge's OWN pristine render at the new path is reclaimable, so the carry
// proceeds over it.
func TestCarryDisownAcrossRename_ReclaimsPristineAtNewPath(t *testing.T) {
	const body = "package config\n\n// mine\n"
	root, cs := carryFixture(t, "pkg/config/config.go", body)
	stamped, ok := Stamp("pkg/config/config_gen.go", []byte("package config\n\n// forge render\n"))
	if !ok {
		t.Fatal("Stamp refused a .go path")
	}
	if err := os.WriteFile(filepath.Join(root, "pkg/config/config_gen.go"), stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	if !CarryDisownAcrossRename(root, cs, "pkg/config/config.go", "pkg/config/config_gen.go") {
		t.Fatal("carry-over should have happened over forge's own render")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "pkg/config/config_gen.go")); string(got) != body {
		t.Errorf("the user's file should now occupy the new path, got %q", got)
	}
}

// An undisowned path is not this mechanism's business.
func TestCarryDisownAcrossRename_NoOpWithoutDisown(t *testing.T) {
	root := t.TempDir()
	cs := &FileChecksums{Disowned: map[string]DisownedEntry{}, Unstampable: map[string]string{}}
	if CarryDisownAcrossRename(root, cs, "a/b.go", "a/b_gen.go") {
		t.Error("no disown means nothing to carry")
	}
}
