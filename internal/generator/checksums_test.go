package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestHashContent(t *testing.T) {
	h1 := HashContent([]byte("hello"))
	h2 := HashContent([]byte("hello"))
	h3 := HashContent([]byte("world"))

	if h1 != h2 {
		t.Errorf("same content should produce same hash")
	}
	if h1 == h3 {
		t.Errorf("different content should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex digest should be 64 chars, got %d", len(h1))
	}
}

// TestWriteGeneratedFile_StampsAndCertifies ports the manifest-era
// RecordFile/IsFileModified round-trip: "forge wrote it" is now proved
// by the embedded marker, and "user modified it" by the marker failing
// verification.
func TestWriteGeneratedFile_StampsAndCertifies(t *testing.T) {
	checksums.ResetSkipWrite()
	root := t.TempDir()
	cs := &FileChecksums{}
	content := []byte("// generated content\npackage f\n")
	relPath := "test/file.go"

	if _, err := WriteGeneratedFile(root, relPath, content, cs, false); err != nil {
		t.Fatal(err)
	}
	fullPath := filepath.Join(root, relPath)
	onDisk, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}

	// File certifies itself (content matches → "not modified").
	if checksums.Verify(onDisk) != checksums.Pristine {
		t.Errorf("freshly written file should verify Pristine, got %v", checksums.Verify(onDisk))
	}

	// Modify the file on disk (marker survives, body changes) — now it
	// must be detected as modified.
	if err := os.WriteFile(fullPath, append(onDisk, []byte("// user modified\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, _ := os.ReadFile(fullPath)
	if checksums.Verify(edited) != checksums.Modified {
		t.Errorf("hand-edited file should verify Modified, got %v", checksums.Verify(edited))
	}
}

// TestVerify_UntrackedFile: a file forge never wrote carries no marker
// — the untracked state (never "modified").
func TestVerify_UntrackedFile(t *testing.T) {
	if got := checksums.Verify([]byte("package x\n")); got != checksums.NoMarker {
		t.Errorf("untracked content should verify NoMarker, got %v", got)
	}
}

// TestScanTier1Drift_DeletedTrackedFile ports "deleted file should not
// be considered modified": a scoped-fallback record whose file is gone
// is not a stomp candidate.
func TestScanTier1Drift_DeletedTrackedFile(t *testing.T) {
	root := t.TempDir()
	cs := &FileChecksums{Unstampable: map[string]string{
		"deleted.json": checksums.BodyHash([]byte("old content")),
	}}
	if drift := checksums.ScanTier1Drift(root, cs); len(drift) != 0 {
		t.Errorf("deleted tracked file should not be considered modified; drift = %+v", drift)
	}
}

func TestLoadSaveChecksums(t *testing.T) {
	root := t.TempDir()

	// Loading from an empty dir returns empty state — a project with no
	// disowns and no comment-incapable outputs has NO forge state files.
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("LoadChecksums from empty dir: %v", err)
	}
	if len(cs.Disowned) != 0 || len(cs.Unstampable) != 0 {
		t.Errorf("expected empty state, got %+v", cs)
	}

	// Save state and round-trip it.
	cs.ForgeVersion = "1.0.0"
	cs.Disowned["a.go"] = DisownedEntry{Reason: "mine now", DisownedAt: "2026-06-11T00:00:00Z"}
	cs.Unstampable["b.json"] = HashContent([]byte("bbb"))

	if err := SaveChecksums(root, cs); err != nil {
		t.Fatalf("SaveChecksums: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, checksums.DisownedFile)); err != nil {
		t.Fatalf("disowned state file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, checksums.HashesFile)); err != nil {
		t.Fatalf("hashes state file should exist: %v", err)
	}

	cs2, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if cs2.Disowned["a.go"] != cs.Disowned["a.go"] {
		t.Errorf("disowned entry didn't round-trip: %+v", cs2.Disowned)
	}
	if cs2.Unstampable["b.json"] != cs.Unstampable["b.json"] {
		t.Errorf("unstampable entry didn't round-trip: %+v", cs2.Unstampable)
	}

	// Emptying the maps and saving DELETES the state files — zero
	// bookkeeping diff in the steady state.
	cs2.Disowned = map[string]DisownedEntry{}
	cs2.Unstampable = map[string]string{}
	if err := SaveChecksums(root, cs2); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{checksums.DisownedFile, checksums.HashesFile} {
		if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
			t.Errorf("%s should be deleted when its map empties (stat err=%v)", f, err)
		}
	}
}

// TestLoadChecksums_IgnoresLegacyManifest: Load never reads the dead
// .forge/checksums.json — only the one-time migration touches it (see
// internal/checksums/migrate_test.go for the legacy flat-shape
// promotion the old TestLoadChecksums_LegacyFlatShape covered).
func TestLoadChecksums_IgnoresLegacyManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"forge_version":"0.5.0","files":{"a.go":"deadbeef"}}`)
	if err := os.WriteFile(filepath.Join(dir, "checksums.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(cs.Disowned) != 0 || len(cs.Unstampable) != 0 {
		t.Errorf("legacy manifest must not populate the new state: %+v", cs)
	}
	// And the file is left for the migration to consume.
	if _, err := os.Stat(filepath.Join(dir, "checksums.json")); err != nil {
		t.Errorf("Load must not delete the legacy manifest: %v", err)
	}
}

func TestWriteGeneratedFile(t *testing.T) {
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState() // unscoped force
	t.Cleanup(checksums.ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}

	content := []byte("// generated code\npackage main\n")

	// First write should succeed and stamp the file.
	written, err := WriteGeneratedFile(root, "main.go", content, cs, false)
	if err != nil {
		t.Fatalf("WriteGeneratedFile: %v", err)
	}
	if !written {
		t.Errorf("first write should succeed")
	}
	got, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if checksums.BodyHash(got) != checksums.BodyHash(content) {
		t.Errorf("file content mismatch (marker-insensitive)")
	}
	if checksums.Verify(got) != checksums.Pristine {
		t.Errorf("written file should self-certify")
	}

	// Second write with a NEW render: the on-disk file is a pristine-but-
	// STALE prior vintage. The current default is non-destructive heal-SKIP
	// — a pristine prior render is byte-indistinguishable from a deliberate
	// user revert, so forge leaves it untouched unless heal/force opts in.
	written, err = WriteGeneratedFile(root, "main.go", []byte("// updated\npackage main\n"), cs, false)
	if err != nil {
		t.Fatalf("WriteGeneratedFile (stale render): %v", err)
	}
	if written {
		t.Errorf("pristine-but-stale render should be heal-SKIPPED by default, not overwritten")
	}
	// On disk is still the prior vintage.
	if got, _ := os.ReadFile(filepath.Join(root, "main.go")); checksums.BodyHash(got) != checksums.BodyHash(content) {
		t.Errorf("stale-skip must leave the prior render on disk")
	}

	// With --heal (AutoHeal) the stale file is advanced to the new render.
	checksums.AutoHeal = true
	written, err = WriteGeneratedFile(root, "main.go", []byte("// updated\npackage main\n"), cs, false)
	checksums.AutoHeal = false
	if err != nil {
		t.Fatalf("WriteGeneratedFile (heal): %v", err)
	}
	if !written {
		t.Errorf("--heal should overwrite a pristine-but-stale render")
	}

	// Now simulate user modification: marker kept, body changed. (A
	// wholesale replace would drop the marker and become the legacy
	// untracked-overwrite path instead.)
	onDisk, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if err := os.WriteFile(filepath.Join(root, "main.go"), append(onDisk, []byte("// user edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write should be skipped (user modified).
	written, err = WriteGeneratedFile(root, "main.go", []byte("// new gen\npackage main\n"), cs, false)
	if err != nil {
		t.Fatalf("WriteGeneratedFile (skip): %v", err)
	}
	if written {
		t.Errorf("write should be skipped when file is user-modified")
	}

	// Force write should override.
	written, err = WriteGeneratedFile(root, "main.go", []byte("// forced\npackage main\n"), cs, true)
	if err != nil {
		t.Fatalf("WriteGeneratedFile (force): %v", err)
	}
	if !written {
		t.Errorf("force write should succeed even when file is user-modified")
	}
}

// TestStampedVintagesStayPristine repoints the manifest-history tests
// (TestRecordFile_TracksHistory / TestRecordFile_HistoryBound): the
// explicit render-history list — and therefore its depth bound — no
// longer exists. The property history provided ("any prior forge
// render is recognized as forge's, not the user's") is now structural:
// every stamped vintage certifies itself, with no per-path state to
// grow or bound.
func TestStampedVintagesStayPristine(t *testing.T) {
	const rel = "x.go"
	for i := 0; i < 8; i++ {
		render := []byte("package x // vintage " + string(rune('a'+i)) + "\n")
		stamped, ok := checksums.Stamp(rel, render)
		if !ok {
			t.Fatal("unstampable")
		}
		if checksums.Verify(stamped) != checksums.Pristine {
			t.Fatalf("vintage %d does not self-certify", i)
		}
		// Stamping is idempotent — re-stamping an old vintage never
		// churns it (the dedupe role the history list used to play).
		again, _ := checksums.Stamp(rel, stamped)
		if string(again) != string(stamped) {
			t.Fatalf("Stamp not idempotent for vintage %d", i)
		}
	}
}

// TestWriteGeneratedFile_HealSkipStaleCodegen — the heal-skip contract.
// A template update produces a v2 render; the on-disk file is still the
// stamped v1 render (stale codegen). Because a pristine prior render is
// byte-indistinguishable from a deliberate user revert, the DEFAULT is
// non-destructive: forge SKIPS the file (does not silently revert the
// user's tree). Overwriting is opt-in only — `--heal` (AutoHeal) for the
// whole tree, or a scoped `--force` for the single path. A genuine user
// edit is detected and skipped regardless.
func TestWriteGeneratedFile_HealSkipStaleCodegen(t *testing.T) {
	checksums.ResetSkipWrite()
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)

	root := t.TempDir()
	cs := &FileChecksums{}
	rel := "stale.go"

	// Initial render: v1 written and stamped.
	v1 := []byte("// rendered v1\npackage s\n")
	if _, err := WriteGeneratedFile(root, rel, v1, cs, false); err != nil {
		t.Fatal(err)
	}

	// Template update: forge now renders v2; the on-disk file is the
	// stale v1 vintage. Default (no heal/force) → SKIP, leave v1 in place.
	v2 := []byte("// rendered v2\npackage s\n")
	wrote, err := WriteGeneratedFile(root, rel, v2, cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Errorf("stale codegen (pristine prior render) must be SKIPPED by default, not overwritten")
	}
	got, _ := os.ReadFile(filepath.Join(root, rel))
	if checksums.BodyHash(got) != checksums.BodyHash(v1) {
		t.Errorf("default heal-skip must leave the v1 render on disk")
	}

	// Opt in via --heal (AutoHeal): the stale file is advanced to v2.
	checksums.AutoHeal = true
	wrote, err = WriteGeneratedFile(root, rel, v2, cs, false)
	checksums.AutoHeal = false
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Errorf("--heal should overwrite a pristine-but-stale render")
	}
	got, _ = os.ReadFile(filepath.Join(root, rel))
	if checksums.BodyHash(got) != checksums.BodyHash(v2) {
		t.Errorf("on-disk not healed to the v2 render under --heal")
	}

	// A real user edit should still be detected and skipped (even though
	// AutoHeal is off — a modified marker is the stomp guard's territory).
	if err := os.WriteFile(filepath.Join(root, rel), append(got, []byte("// user wrote this\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err = WriteGeneratedFile(root, rel, []byte("// rendered v3\npackage s\n"), cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Errorf("genuine user edit should still be flagged as modified and skipped")
	}
}

// TestWriteGeneratedFile_CreatesSubdirectories verifies that nested
// destination paths get their parent directories created.
func TestWriteGeneratedFile_CreatesSubdirectories(t *testing.T) {
	checksums.ResetSkipWrite()
	root := t.TempDir()
	cs := &FileChecksums{}

	content := []byte("package pkg\n")
	written, err := WriteGeneratedFile(root, "deep/nested/dir/file.go", content, cs, false)
	if err != nil {
		t.Fatalf("WriteGeneratedFile: %v", err)
	}
	if !written {
		t.Errorf("write should succeed")
	}

	got, err := os.ReadFile(filepath.Join(root, "deep/nested/dir/file.go"))
	if err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	if checksums.BodyHash(got) != checksums.BodyHash(content) {
		t.Errorf("content mismatch")
	}
}
