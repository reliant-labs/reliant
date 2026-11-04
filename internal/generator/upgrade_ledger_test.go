package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// reliant's cmd/reliant/cmd/metrics.go and commands.go were recorded deleted
// in .forge/scaffolded.json, and `forge project upgrade` wrote them again (they
// no longer compiled: `undefined: version`). The templates phase treated every
// absent managed file as "needs adopting"; for a scaffold-once (Tier-2) file
// absence is the project's decision, recorded in the birth ledger.
//
// The subject is derived from the manifest, not named: every Tier-2 path the
// upgrade manages, recorded as deleted, must stay absent.
func TestUpgrade_NeverRecreatesScaffoldOnceFilesTheLedgerRecordsDeleted(t *testing.T) {
	cfg := testProjectConfig()
	dir := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	var tier2 []string
	for _, f := range filterManagedFiles(managedFilesForCfg(cfg), cfg) {
		if f.tier == Tier2 {
			tier2 = append(tier2, f.destPath)
			checksums.RecordScaffold(dir, f.destPath)
		}
	}
	// The two files from the report must be in the derived set, or this test
	// has stopped looking at what it was written for.
	for _, want := range []string{
		cmdTreePath(cfg.Name, "metrics.go"), cmdTreePath(cfg.Name, "commands.go"),
	} {
		if !containsPath(tier2, want) {
			t.Fatalf("Tier-2 manifest lost %s: %v", want, tier2)
		}
	}

	results, err := UpgradeSelection(dir, cfg, ForceNone(), false)
	if err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	for _, rel := range tier2 {
		if _, statErr := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(statErr) {
			t.Errorf("upgrade re-created %s, which the ledger records as deleted (stat err %v)", rel, statErr)
		}
	}
	for _, r := range results {
		if containsPath(tier2, r.Path) && (!r.LeftDeleted || r.Status != UpgradeSkipped) {
			t.Errorf("%s: status %q leftDeleted=%v, want a skip that says it was left deleted", r.Path, r.Status, r.LeftDeleted)
		}
	}
}

// A disowned scaffold-once path that is absent is the project's too.
func TestUpgrade_NeverRecreatesADisownedAbsentScaffoldOnceFile(t *testing.T) {
	cfg := testProjectConfig()
	dir := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	rel := cmdTreePath(cfg.Name, "metrics.go")
	cs := &FileChecksums{Disowned: map[string]DisownedEntry{filepath.ToSlash(rel): {Reason: "mine"}}}
	if err := checksums.Save(dir, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeSelection(dir, cfg, ForceNone(), false); err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
		t.Fatalf("upgrade re-created disowned %s (stat err %v)", rel, err)
	}
}

// The other side: a scaffold-once file forge has never written is still born by
// upgrade (that is how an older project gains a newer scaffold), and the birth
// is recorded so deleting it afterwards sticks.
func TestUpgrade_BirthsAnUnrecordedScaffoldOnceFileAndRecordsIt(t *testing.T) {
	cfg := testProjectConfig()
	dir := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	rel := cmdTreePath(cfg.Name, "metrics.go")
	if _, err := UpgradeSelection(dir, cfg, ForceNone(), false); err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("upgrade did not birth %s: %v", rel, err)
	}
	if !checksums.ScaffoldRecorded(dir, rel) {
		t.Fatalf("upgrade's birth of %s was not recorded in the ledger", rel)
	}

	if err := os.Remove(filepath.Join(dir, rel)); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeSelection(dir, cfg, ForceNone(), false); err != nil {
		t.Fatalf("UpgradeSelection (second): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
		t.Fatalf("a deletion after upgrade's birth did not stick (stat err %v)", err)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if filepath.ToSlash(p) == filepath.ToSlash(want) {
			return true
		}
	}
	return false
}
