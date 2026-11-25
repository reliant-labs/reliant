// Renames must never void a disown (fr-disown-rename).
//
// `forge project disown` is the sanctioned one-way exit: after it, the path is
// the user's and forge never writes it again. A later rename of the emitter's
// OUTPUT path used to re-key the emit onto a name the disown record did not
// cover, so forge emitted a SECOND file declaring the same symbols beside the
// user's — a package that does not compile, produced silently.
//
// These tests pin the property that makes that impossible: after any renamed
// emitter runs over a disowned old path, the package holds exactly ONE
// definition of the emitter's symbols, and it is the user's copy.
package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// disownedProject writes a user-owned file at relPath (no forge marker — the
// disown strips it) and returns a tracker that records the transfer, matching
// what `forge project disown` leaves on disk.
func disownedProject(t *testing.T, dir, relPath, body string) *checksums.FileChecksums {
	t.Helper()
	// The write-block set is per-RUN global state (like Tier1TargetSet), so
	// each test must start from a clean one or a refusal in one test
	// suppresses a legitimate write in the next.
	checksums.ResetWriteBlocks()
	t.Cleanup(checksums.ResetWriteBlocks)
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &checksums.FileChecksums{
		Disowned: map[string]checksums.DisownedEntry{
			filepath.ToSlash(relPath): {Reason: "hand-tuned for the migration", DisownedAt: "2026-01-01T00:00:00Z"},
		},
		Unstampable: map[string]string{},
	}
}

// TestConfigLoaderRename_DisownedFileIsNotDuplicated is the reported symptom on
// pkg/config: the user disowned pkg/config/config.go, forge later renamed the
// emitter's output to config_gen.go, and the project ended up with BOTH files
// declaring package config's Config/Load/RegisterFlags.
func TestConfigLoaderRename_DisownedFileIsNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const userBody = "package config\n\n// hand-written by the user after disowning.\ntype Config struct{ Port int }\n"
	oldRel := filepath.Join("pkg", "config", "config.go")
	cs := disownedProject(t, dir, oldRel, userBody)

	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, cs); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}

	oldExists := fileExists(t, filepath.Join(dir, oldRel))
	newExists := fileExists(t, filepath.Join(dir, "pkg", "config", "config_gen.go"))
	if oldExists && newExists {
		t.Fatal("COLLISION: forge emitted pkg/config/config_gen.go beside the disowned pkg/config/config.go — " +
			"both declare package config's Config/Load, so the package does not compile")
	}
	if !oldExists && !newExists {
		t.Fatal("the user's disowned config was deleted — a disown must never lose the user's work")
	}

	// Whichever single path survives must hold the USER's bytes, never a
	// fresh render: a rename may relocate a disowned file, never overwrite it.
	surviving := filepath.Join(dir, oldRel)
	if !oldExists {
		surviving = filepath.Join(dir, "pkg", "config", "config_gen.go")
	}
	got, err := os.ReadFile(surviving)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != userBody {
		t.Errorf("disowned file was overwritten by a fresh render.\n got: %q\nwant: %q", got, userBody)
	}

	// The disown record must still cover wherever the file now lives,
	// otherwise the NEXT run re-emits over it and the collision returns.
	rel, err := filepath.Rel(dir, surviving)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsDisowned(filepath.ToSlash(rel)) {
		t.Errorf("disown record does not cover the surviving path %q — the next run would re-emit over it", rel)
	}
}

// TestInventoryRename_DisownedFileIsNotDuplicated is the exact case from the
// migration: internal/app/mounts_services.go was disowned, forge renamed the
// emitter to mounts_services_gen.go, and both landed in package app.
func TestInventoryRename_DisownedFileIsNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	const userBody = "package app\n\n// user-owned service mounting.\nfunc MountServices() {}\n"
	oldRel := filepath.Join("internal", "app", "mounts_services.go")
	cs := disownedProject(t, dir, oldRel, userBody)

	err := GenerateInventory(InventoryGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj", Checksums: cs},
	})
	if err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}

	oldExists := fileExists(t, filepath.Join(dir, oldRel))
	newExists := fileExists(t, filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if oldExists && newExists {
		t.Fatal("COLLISION: forge emitted internal/app/mounts_services_gen.go beside the disowned " +
			"internal/app/mounts_services.go — package app declares MountServices twice")
	}
	if !oldExists && !newExists {
		t.Fatal("the user's disowned mounts file was deleted — a disown must never lose the user's work")
	}
}

// A rename over a path the user has NOT disowned must keep working exactly as
// before: forge's own pristine copy is reclaimed and the new name is emitted.
func TestRename_UndisownedPristineFileIsStillRetired(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &checksums.FileChecksums{
		Disowned:    map[string]checksums.DisownedEntry{},
		Unstampable: map[string]string{},
	}
	// Seed a pristine forge render at the OLD path by generating once at the
	// old name, then let the renamed emitter reclaim it.
	oldFull := filepath.Join(dir, "pkg", "config", "config.go")
	if err := os.MkdirAll(filepath.Dir(oldFull), 0o755); err != nil {
		t.Fatal(err)
	}
	stamped, ok := checksums.Stamp("pkg/config/config.go", []byte("package config\n\n// forge render\n"))
	if !ok {
		t.Fatal("Stamp refused a .go path")
	}
	if err := os.WriteFile(oldFull, stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, cs); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}

	if fileExists(t, oldFull) {
		t.Error("forge's own pristine copy at the old path must be reclaimed by the rename")
	}
	if !fileExists(t, filepath.Join(dir, "pkg", "config", "config_gen.go")) {
		t.Error("the renamed emitter must still write pkg/config/config_gen.go")
	}
}

// The refusal case: forge renamed an emitter's output onto a path the user
// ALREADY has a file at, and that file is not forge's to remove. Forge must
// not overwrite it and must not silently drop the disown — it reports a
// runbook and leaves both files (and the protection) intact.
func TestRename_DisownCarryConflict_RefusesLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const userOld = "package config\n\n// the disowned original.\ntype Config struct{ Port int }\n"
	const userNew = "package config\n\n// an unrelated hand-written file the user put at the new name.\nfunc Helper() {}\n"
	oldRel := filepath.Join("pkg", "config", "config.go")
	cs := disownedProject(t, dir, oldRel, userOld)
	newFull := filepath.Join(dir, "pkg", "config", "config_gen.go")
	if err := os.WriteFile(newFull, []byte(userNew), 0o644); err != nil {
		t.Fatal(err)
	}

	var conflicts [][2]string
	prev := checksums.DisownCarryConflictFn
	checksums.DisownCarryConflictFn = func(o, n string) { conflicts = append(conflicts, [2]string{o, n}) }
	t.Cleanup(func() { checksums.DisownCarryConflictFn = prev })

	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, cs); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}

	if len(conflicts) != 1 {
		t.Fatalf("expected exactly one loud conflict report, got %v", conflicts)
	}

	// Neither file may be touched.
	if got, _ := os.ReadFile(filepath.Join(dir, oldRel)); string(got) != userOld {
		t.Errorf("disowned original was modified: %q", got)
	}
	if got, _ := os.ReadFile(newFull); string(got) != userNew {
		t.Errorf("the user's file at the new path was overwritten: %q", got)
	}
	// And the protection must survive, or the next run silently stomps it.
	if !cs.IsDisowned(filepath.ToSlash(oldRel)) {
		t.Error("the disown record must be kept when the carry-over is refused")
	}
}

// Re-adoption by deletion still works across a rename: the user deleted their
// disowned copy, which hands the path back to forge, so forge emits the new
// name cleanly and the dead record does not linger.
func TestRename_DisownedFileDeleted_ReadoptsUnderNewName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRel := filepath.Join("pkg", "config", "config.go")
	cs := disownedProject(t, dir, oldRel, "package config\n")
	// The re-adoption gesture: delete the file, keep the record.
	if err := os.Remove(filepath.Join(dir, oldRel)); err != nil {
		t.Fatal(err)
	}

	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, cs); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}

	if !fileExists(t, filepath.Join(dir, "pkg", "config", "config_gen.go")) {
		t.Error("forge must re-adopt and emit the new name after the user deleted their disowned copy")
	}
	if cs.IsDisowned(filepath.ToSlash(oldRel)) {
		t.Error("the disown record for a deleted file must not survive the rename")
	}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return false
}
