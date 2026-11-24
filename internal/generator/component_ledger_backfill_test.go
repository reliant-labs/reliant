package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// A frontend scaffolded before the birth ledger existed has its core
// components on disk and no records. Deleting one used to read as "never
// scaffolded", so the next generate wrote it again (with a fresh record) and
// `forge generate --check` reported the rebirth as drift. The first run now
// backfills a record for each present component, so the deletion holds.
func TestEnsureCoreComponentsBackfillsPreLedgerComponents(t *testing.T) {
	root := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: app\nmodule_path: example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fe := filepath.Join(root, "frontends", "web")
	ui := filepath.Join(fe, "src", "components", "ui")
	if err := os.MkdirAll(ui, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-ledger: the component is on disk, the ledger holds nothing.
	const rel = "frontends/web/src/components/ui/chip.tsx"
	chip := filepath.Join(root, filepath.FromSlash(rel))
	const old = "export default function Chip() { return null; }\n"
	if err := os.WriteFile(chip, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureCoreComponentsIn(fe, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(chip); string(got) != old {
		t.Fatalf("a present component was rewritten:\n%s", got)
	}
	if !checksums.ScaffoldRecorded(root, rel) {
		t.Fatalf("%s is present but has no birth record after generate, so deleting it would bring it back", rel)
	}

	// The user deletes it; the next run respects that.
	if err := os.Remove(chip); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCoreComponentsIn(fe, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chip); !os.IsNotExist(err) {
		t.Fatalf("deleted %s was scaffolded again (stat err %v)", rel, err)
	}
	if absent := checksums.AbsentScaffolds(root); len(absent) != 1 || absent[0] != rel {
		t.Fatalf("AbsentScaffolds = %v, want [%s]", absent, rel)
	}
}
