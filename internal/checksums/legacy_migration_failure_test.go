package checksums

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// failWritesTo makes writeStateFile fail for any path ending in one of
// rels (slash-separated) until the returned restore runs (or the test
// ends). A seam rather than permission bits, which Windows does not have.
func failWritesTo(t *testing.T, rels ...string) (restore func()) {
	t.Helper()
	orig := writeStateFile
	writeStateFile = func(path string, data []byte, perm fs.FileMode) error {
		for _, rel := range rels {
			if strings.HasSuffix(filepath.ToSlash(path), rel) {
				return errors.New("injected write failure")
			}
		}
		return orig(path, data, perm)
	}
	restore = func() { writeStateFile = orig }
	t.Cleanup(restore)
	return restore
}

const (
	legacyLedger    = `{"files": {"a.go": {"scaffolded_at": "2026-10-01T00:00:00Z"}, "gone.go": {}}, "absent_reported": ["gone.go"]}`
	legacyGitignore = ".forge/*\n!.forge/scaffolded.json\n!.forge/hashes.json\n"
)

// A legacy scaffold ledger whose entries could not all be written stays,
// byte for byte, with the .gitignore line that commits it: it is the only
// complete copy of the history. The next save that succeeds migrates it.
func TestScaffoldLedger_FailedEntryWriteKeepsTheLegacyLedger(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, LegacyScaffoldedFile, legacyLedger)
	writeT(t, root, ".gitignore", legacyGitignore)
	writeT(t, root, "a.go", "package a\n")
	writeT(t, root, "b.go", "package a\n")
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)

	restore := failWritesTo(t, ScaffoldEntryPath("a.go"))
	RecordScaffold(root, "b.go")
	if got, ok := readT(t, root, LegacyScaffoldedFile); !ok || got != legacyLedger {
		t.Fatalf("the legacy ledger must stay intact when an entry write failed: present=%v\n%s", ok, got)
	}
	if gi, _ := readT(t, root, ".gitignore"); gi != legacyGitignore {
		t.Errorf(".gitignore must still commit the legacy ledger:\n%s", gi)
	}

	restore()
	ResetScaffoldLedgerCache()
	RecordScaffold(root, "c.go")
	ResetScaffoldLedgerCache()
	if _, ok := readT(t, root, LegacyScaffoldedFile); ok {
		t.Error("a save that persists everything must still migrate the legacy ledger")
	}
	for _, p := range []string{"a.go", "gone.go", "b.go", "c.go"} {
		if !ScaffoldRecorded(root, p) {
			t.Errorf("%s lost by the retried migration", p)
		}
	}
	if entry, _ := readT(t, root, ScaffoldEntryPath("gone.go")); !strings.Contains(entry, "absent_reported") {
		t.Errorf("gone.go's reported absence lost: %s", entry)
	}
}

// A legacy ledger whose entries were written but whose .gitignore line could
// not be moved stays: the entries are not committed yet.
func TestScaffoldLedger_FailedGitignoreWriteKeepsTheLegacyLedger(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, LegacyScaffoldedFile, legacyLedger)
	writeT(t, root, ".gitignore", legacyGitignore)
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)

	failWritesTo(t, ".gitignore")
	RecordScaffold(root, "b.go")
	if got, ok := readT(t, root, LegacyScaffoldedFile); !ok || got != legacyLedger {
		t.Fatalf("the legacy ledger must stay when the .gitignore migration failed: present=%v", ok)
	}
}

// A failed certificate write fails Save and keeps the legacy hashes
// manifest and the .gitignore line that commits it.
func TestHashCerts_FailedCertificateWriteKeepsTheLegacyManifest(t *testing.T) {
	root := t.TempDir()
	h := Hash([]byte("x"))
	legacy := `{"files": {"web/schema_gen.json": "` + h + `"}}` + "\n"
	writeT(t, root, LegacyHashesFile, legacy)
	writeT(t, root, ".gitignore", legacyGitignore)
	cs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	failWritesTo(t, HashCertPath("web/schema_gen.json"))
	if err := Save(root, cs); err == nil {
		t.Fatal("Save must fail when a certificate cannot be written")
	}
	if got, ok := readT(t, root, LegacyHashesFile); !ok || got != legacy {
		t.Errorf("the legacy manifest must stay intact: present=%v", ok)
	}
	if gi, _ := readT(t, root, ".gitignore"); gi != legacyGitignore {
		t.Errorf(".gitignore must still commit the legacy manifest:\n%s", gi)
	}
}

// A failed .gitignore migration fails Save and keeps the legacy manifest:
// the certificates exist but nothing commits them yet.
func TestHashCerts_FailedGitignoreWriteKeepsTheLegacyManifest(t *testing.T) {
	root := t.TempDir()
	h := Hash([]byte("x"))
	legacy := `{"files": {"web/schema_gen.json": "` + h + `"}}` + "\n"
	writeT(t, root, LegacyHashesFile, legacy)
	writeT(t, root, ".gitignore", legacyGitignore)
	cs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	failWritesTo(t, ".gitignore")
	if err := Save(root, cs); err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("Save = %v, want the .gitignore write error", err)
	}
	if got, ok := readT(t, root, LegacyHashesFile); !ok || got != legacy {
		t.Errorf("the legacy manifest must stay intact: present=%v", ok)
	}
}

// A legacy manifest whose hashes are all dropped from the current set (each
// output now uncertified) is deleted only with its .gitignore line moved to
// the certificate directory, so certificates written later are committed.
func TestHashCerts_EmptySetStillMovesTheGitignoreLine(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, LegacyHashesFile, `{"files": {"web/schema_gen.json": "`+Hash([]byte("x"))+`"}}`+"\n")
	writeT(t, root, ".gitignore", legacyGitignore)
	if err := saveHashCerts(root, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	gi, _ := readT(t, root, ".gitignore")
	if strings.Contains(gi, "!.forge/hashes.json") || !strings.Contains(gi, "!.forge/hashes/\n") {
		t.Errorf("the legacy manifest went but .gitignore still commits only it:\n%s", gi)
	}
	if _, ok := readT(t, root, LegacyHashesFile); ok {
		t.Error("the legacy manifest must go once its line is moved")
	}

	// The same with a failed .gitignore write: the manifest stays.
	root = t.TempDir()
	writeT(t, root, LegacyHashesFile, `{"files": {}}`)
	writeT(t, root, ".gitignore", legacyGitignore)
	failWritesTo(t, ".gitignore")
	if err := saveHashCerts(root, map[string]string{}); err == nil {
		t.Fatal("saveHashCerts must fail when the .gitignore line cannot move")
	}
	if _, ok := readT(t, root, LegacyHashesFile); !ok {
		t.Error("the legacy manifest must stay while .gitignore still commits it")
	}
}
