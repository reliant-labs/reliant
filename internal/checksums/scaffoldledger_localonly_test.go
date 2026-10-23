package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLocalOnlyScaffold(t *testing.T) {
	for rel, want := range map[string]bool{
		"secrets/dev.yaml":                  true,
		"secrets/e2e.yaml":                  true,
		"secrets/.yaml":                     false,
		"secrets/dev.yml":                   false,
		"secrets/sub/dev.yaml":              false,
		"internal/secrets/dev.yaml":         false,
		"deploy/kcl/dev/config.k":           false,
		"frontends/web/src/app/secrets/p.x": false,
	} {
		if got := IsLocalOnlyScaffold(rel); got != want {
			t.Errorf("IsLocalOnlyScaffold(%q) = %v, want %v", rel, got, want)
		}
	}
}

// A local-only path is never recorded, so presence alone decides its birth:
// absent means write, present means leave — never "deleted, leave deleted".
func TestScaffoldLedger_LocalOnlyPathIsNeverRecorded(t *testing.T) {
	ResetScaffoldLedgerCache()
	root := t.TempDir()
	const rel = "secrets/dev.yaml"

	RecordScaffold(root, rel)
	RecordScaffoldWithHash(root, rel, []byte("x"))
	if ScaffoldRecorded(root, rel) {
		t.Fatal("a local-only path was recorded")
	}
	if !ScaffoldOnceDecision(root, rel) {
		t.Fatal("absent local-only path must be a birth")
	}
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("A: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ScaffoldOnceDecision(root, rel) {
		t.Fatal("present local-only path must be left alone")
	}
	if _, err := os.Stat(filepath.Join(root, ScaffoldedFile)); !os.IsNotExist(err) {
		t.Fatalf("ledger file written for a local-only path only: %v", err)
	}
}
