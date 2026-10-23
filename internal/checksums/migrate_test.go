// Tests for the one-time legacy-manifest migration (migrate.go):
// .forge/checksums.json → self-certifying markers + the two small
// state files.
package checksums

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLegacyManifest marshals a LegacyManifest to .forge/checksums.json.
func writeLegacyManifest(t *testing.T, root string, m any) {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, LegacyChecksumFile)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFileAt(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyManifest_NoManifestIsNoOp(t *testing.T) {
	root := t.TempDir()
	cs := &FileChecksums{Disowned: map[string]DisownedEntry{}, Unstampable: map[string]string{}}
	out, err := MigrateLegacyManifest(root, cs, nil)
	if err != nil {
		t.Fatalf("MigrateLegacyManifest: %v", err)
	}
	if out != nil {
		t.Errorf("outcome = %+v, want nil when there is no legacy manifest", out)
	}
}

func TestMigrateLegacyManifest_Outcomes(t *testing.T) {
	orig := nowRFC3339
	nowRFC3339 = func() string { return "2026-06-11T00:00:00Z" }
	defer func() { nowRFC3339 = orig }()

	root := t.TempDir()

	current := []byte("package a // current render\n")
	older := []byte("package a // older render\n")
	unknown := []byte("package a // nothing recorded matches this\n")
	jsonBody := []byte("{\"a\":1}\n")

	writeFileAt(t, root, "match_current.go", current)
	writeFileAt(t, root, "match_history.go", older)
	writeFileAt(t, root, "unverified.go", unknown)
	writeFileAt(t, root, "disowned.go", []byte("package d // user's\n"))
	writeFileAt(t, root, "forked.go", []byte("package f // user's\n"))
	writeFileAt(t, root, "tier2.go", []byte("package t // scaffold\n"))
	writeFileAt(t, root, "dropped.go", current)
	writeFileAt(t, root, "config/app.json", jsonBody)
	// "missing.go" and "disowned_gone.go" deliberately not on disk.

	// Scaffold-era .gitignore negation that must be rewritten.
	writeFileAt(t, root, ".gitignore", []byte(".forge/\n!"+LegacyChecksumFile+"\nnode_modules/\n"))

	writeLegacyManifest(t, root, LegacyManifest{
		ForgeVersion: "0.9.0",
		Files: map[string]LegacyEntry{
			"match_current.go": {Hash: Hash(current), History: []string{Hash(current)}},
			"match_history.go": {Hash: Hash(current), History: []string{Hash(older), Hash(current)}},
			"unverified.go":    {Hash: Hash(current), History: []string{Hash(current)}},
			"disowned.go":      {Hash: "x", Tier: 2, Disowned: true, DisownedAt: "2026-01-02T03:04:05Z"},
			"forked.go":        {Hash: "x", Tier: 1, Forked: true, ForkedAt: "2026-02-03T04:05:06Z"},
			"disowned_gone.go": {Hash: "x", Tier: 2, Disowned: true},
			"tier2.go":         {Hash: Hash([]byte("anything")), Tier: 2},
			"dropped.go":       {Hash: Hash(current)},
			"missing.go":       {Hash: Hash(current)},
			"config/app.json":  {Hash: Hash(jsonBody)},
		},
	})

	cs := &FileChecksums{Disowned: map[string]DisownedEntry{}, Unstampable: map[string]string{}}
	predicate := func(rel string) bool { return rel != "dropped.go" }
	out, err := MigrateLegacyManifest(root, cs, predicate)
	if err != nil {
		t.Fatalf("MigrateLegacyManifest: %v", err)
	}
	if out == nil {
		t.Fatal("nil outcome with a legacy manifest present")
	}

	// match-current and match-history-only → stamped pristine.
	wantSorted := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s = %v, want %v", name, got, want)
				return
			}
		}
	}
	wantSorted("Stamped", out.Stamped, "match_current.go", "match_history.go")
	wantSorted("Fallback", out.Fallback, "config/app.json")
	wantSorted("DisownedConverted", out.DisownedConverted, "disowned.go", "forked.go")
	wantSorted("DroppedTier2", out.DroppedTier2, "tier2.go")
	wantSorted("DroppedUnknown", out.DroppedUnknown, "dropped.go")
	wantSorted("MissingOnDisk", out.MissingOnDisk, "disowned_gone.go", "missing.go")
	wantSorted("Unverified", out.Unverified, "unverified.go")
	if got, want := out.Total(), 10; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}

	for _, rel := range []string{"match_current.go", "match_history.go"} {
		got, _ := os.ReadFile(filepath.Join(root, rel))
		if Verify(got) != Pristine {
			t.Errorf("%s: Verify = %v, want Pristine after stamping", rel, Verify(got))
		}
	}
	// Unverified / dropped / tier-2 files keep their bytes unmarked —
	// the CALLER decides what to do with unverified paths.
	for _, rel := range []string{"unverified.go", "dropped.go", "tier2.go"} {
		got, _ := os.ReadFile(filepath.Join(root, rel))
		if _, found := ExtractMarker(got); found {
			t.Errorf("%s must not be stamped by the migration", rel)
		}
	}

	// Disowned / legacy-forked entries convert to disowned records with
	// their original timestamps; the gone one leaves no record
	// (deletion is the documented re-adoption signal).
	if e := cs.Disowned["disowned.go"]; e.DisownedAt != "2026-01-02T03:04:05Z" || e.Reason == "" {
		t.Errorf("disowned.go record = %+v", e)
	}
	if e := cs.Disowned["forked.go"]; e.DisownedAt != "2026-02-03T04:05:06Z" || !strings.Contains(e.Reason, "fork") {
		t.Errorf("forked.go record = %+v, want fork-era reason + original timestamp", e)
	}
	if _, ok := cs.Disowned["disowned_gone.go"]; ok {
		t.Error("disowned-but-deleted entry must leave no record")
	}
	if len(cs.Disowned) != 2 {
		t.Errorf("cs.Disowned = %+v, want exactly the two converted entries", cs.Disowned)
	}

	// Comment-incapable pristine entry → scoped fallback.
	if got := cs.Unstampable["config/app.json"]; got != BodyHash(jsonBody) {
		t.Errorf("Unstampable[config/app.json] = %q, want body hash of the render", got)
	}

	// The legacy manifest is DELETED.
	if _, err := os.Stat(filepath.Join(root, LegacyChecksumFile)); !os.IsNotExist(err) {
		t.Errorf("legacy manifest must be deleted (stat err=%v)", err)
	}

	// The .gitignore negation is rewritten to the new state files.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Contains(string(gi), "!"+LegacyChecksumFile) {
		t.Errorf(".gitignore still negates the dead manifest:\n%s", gi)
	}
	if !strings.Contains(string(gi), "!"+DisownedFile) || !strings.Contains(string(gi), "!"+HashesFile) {
		t.Errorf(".gitignore missing the new negations:\n%s", gi)
	}
	if !strings.Contains(string(gi), "node_modules/") {
		t.Errorf("user .gitignore content lost:\n%s", gi)
	}
}

// TestLoadLegacyManifest_FlatShape: the original flat wire shape
// (files: path → hex string) is accepted and promoted, and the
// migration stamps a matching file from it.
func TestLoadLegacyManifest_FlatShape(t *testing.T) {
	root := t.TempDir()
	content := []byte("package a\n")
	writeFileAt(t, root, "a.go", content)
	writeLegacyManifest(t, root, map[string]any{
		"forge_version": "0.5.0",
		"files": map[string]string{
			"a.go": Hash(content),
		},
	})

	m, err := LoadLegacyManifest(root)
	if err != nil {
		t.Fatalf("LoadLegacyManifest: %v", err)
	}
	if m == nil {
		t.Fatal("nil manifest")
	}
	entry, ok := m.Files["a.go"]
	if !ok {
		t.Fatal("a.go missing from promoted flat manifest")
	}
	if entry.Hash != Hash(content) {
		t.Errorf("Hash = %q, want legacy hex", entry.Hash)
	}
	if len(entry.History) != 1 || entry.History[0] != entry.Hash {
		t.Errorf("History = %v, want seeded with the legacy hash", entry.History)
	}
	if m.ForgeVersion != "0.5.0" {
		t.Errorf("ForgeVersion = %q", m.ForgeVersion)
	}

	cs := &FileChecksums{Disowned: map[string]DisownedEntry{}, Unstampable: map[string]string{}}
	out, err := MigrateLegacyManifest(root, cs, nil)
	if err != nil {
		t.Fatalf("MigrateLegacyManifest: %v", err)
	}
	if len(out.Stamped) != 1 || out.Stamped[0] != "a.go" {
		t.Errorf("Stamped = %v, want [a.go]", out.Stamped)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if Verify(got) != Pristine {
		t.Errorf("flat-shape entry not stamped pristine; Verify = %v", Verify(got))
	}
}

// TestStampUnverified: the sentinel marker never verifies, so the file
// stays Modified and the stomp guard names it (flagged Unverified)
// until the user resolves it.
func TestStampUnverified(t *testing.T) {
	root := t.TempDir()
	const rel = "mystery.go"
	writeFileAt(t, root, rel, []byte("package m // provenance unknown\n"))

	if !StampUnverified(root, rel) {
		t.Fatal("StampUnverified returned false for a stampable file")
	}
	got, _ := os.ReadFile(filepath.Join(root, rel))
	if Verify(got) != Modified {
		t.Errorf("Verify = %v, want Modified (sentinel never verifies)", Verify(got))
	}
	embedded, _ := ExtractMarker(got)
	if embedded != UnverifiedMarkerValue {
		t.Errorf("embedded marker = %q, want the sentinel", embedded)
	}

	cs := &FileChecksums{}
	drift := ScanTier1Drift(root, cs)
	if len(drift) != 1 {
		t.Fatalf("drift = %+v, want exactly the sentinel-stamped file", drift)
	}
	if drift[0].Path != rel || !drift[0].Unverified {
		t.Errorf("drift[0] = %+v, want Path=%s Unverified=true", drift[0], rel)
	}

	// Unstampable formats and missing files are no-ops.
	if StampUnverified(root, "missing.go") {
		t.Error("StampUnverified on a missing file must return false")
	}
	writeFileAt(t, root, "data.json", []byte("{}\n"))
	if StampUnverified(root, "data.json") {
		t.Error("StampUnverified on an unstampable format must return false")
	}
}

// TestRescueUnverified: a parked side render whose BODY matches the
// on-disk bytes proves pristineness — the file is stamped for real.
func TestRescueUnverified(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/wire_gen.go"
	onDisk := []byte("package app // committed bytes\n")
	writeFileAt(t, root, rel, onDisk)

	// No side render parked → no rescue.
	if RescueUnverified(root, rel) {
		t.Error("rescue without a parked render must fail")
	}

	// Parked render whose body MISMATCHES → no rescue, file untouched.
	if err := WriteSideRenderNoBase(root, rel, []byte("package app // different render\n")); err != nil {
		t.Fatal(err)
	}
	if RescueUnverified(root, rel) {
		t.Error("rescue with a mismatching render must fail")
	}
	got, _ := os.ReadFile(filepath.Join(root, rel))
	if string(got) != string(onDisk) {
		t.Errorf("failed rescue modified the file: %q", got)
	}
	// The parked render is consumed either way.
	if _, err := os.Stat(filepath.Join(root, RenderDir, rel)); !os.IsNotExist(err) {
		t.Errorf("side render not cleaned after rescue attempt (stat err=%v)", err)
	}

	// Parked render whose body MATCHES (markers excluded from the body
	// hash, so a stamped render still matches raw on-disk bytes) →
	// rescued: stamped pristine.
	stamped, _ := Stamp(rel, onDisk)
	if err := WriteSideRenderNoBase(root, rel, stamped); err != nil {
		t.Fatal(err)
	}
	if !RescueUnverified(root, rel) {
		t.Fatal("rescue with a body-matching render must succeed")
	}
	got, _ = os.ReadFile(filepath.Join(root, rel))
	if Verify(got) != Pristine {
		t.Errorf("rescued file Verify = %v, want Pristine", Verify(got))
	}
	if BodyHash(got) != BodyHash(onDisk) {
		t.Errorf("rescue changed the body")
	}
}
