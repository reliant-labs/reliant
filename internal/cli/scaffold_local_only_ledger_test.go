package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

const localOnlyStore = "secrets/dev.yaml"

// regenerateEnvConfig re-runs the per-env config step against dir with a cold
// ledger cache, the way a fresh `forge generate` process would.
func regenerateEnvConfig(t *testing.T, dir string) {
	t.Helper()
	checksums.ResetScaffoldLedgerCache()
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	cs, err := generator.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := generatePerEnvDeployConfig(dir, cfg, cs, nil); err != nil {
			t.Fatalf("generatePerEnvDeployConfig: %v", err)
		}
	})
}

func readLedgerBytes(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, checksums.ScaffoldedFile))
	if err != nil {
		t.Fatalf("read scaffold ledger: %v", err)
	}
	return b
}

// TestScaffoldLedger_GitignoredSecretStoreIsNotRecorded: `forge generate` is a
// pure function of COMMITTED inputs. secrets/dev.yaml is gitignored, so the
// committed .forge/scaffolded.json must come out byte-identical whether the
// file exists in the checkout (a developer's tree) or not (a clean clone, CI) —
// and a clean clone must still get the store written.
func TestScaffoldLedger_GitignoredSecretStoreIsNotRecorded(t *testing.T) {
	dir := scaffoldWorkloadModelProject(t)
	store := filepath.Join(dir, filepath.FromSlash(localOnlyStore))
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("scaffold did not seed %s: %v", localOnlyStore, err)
	}
	committed := readLedgerBytes(t, dir)
	var led struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(committed, &led); err != nil {
		t.Fatal(err)
	}
	if _, ok := led.Files[localOnlyStore]; ok {
		t.Fatalf("%s is gitignored but was recorded in the committed ledger", localOnlyStore)
	}

	// A developer's checkout: the file is present.
	regenerateEnvConfig(t, dir)
	if got := readLedgerBytes(t, dir); !bytes.Equal(got, committed) {
		t.Errorf("ledger changed when generate ran with %s present:\n--- before\n%s\n--- after\n%s", localOnlyStore, committed, got)
	}

	// A clean checkout: the file is gone.
	if err := os.RemoveAll(filepath.Join(dir, "secrets")); err != nil {
		t.Fatal(err)
	}
	regenerateEnvConfig(t, dir)
	if got := readLedgerBytes(t, dir); !bytes.Equal(got, committed) {
		t.Errorf("ledger changed when generate ran with %s absent:\n--- before\n%s\n--- after\n%s", localOnlyStore, committed, got)
	}
	if _, err := os.Stat(store); err != nil {
		t.Errorf("a clean checkout must get %s written, not an empty secrets/ dir: %v", localOnlyStore, err)
	}
}

// TestScaffoldLedger_LegacyLocalOnlyEntryConverges: a project whose committed
// ledger still carries secrets/dev.yaml (written by an older forge) converges
// on its next generate — the entry, and its absent_reported echo, are dropped,
// and a clean clone that the entry used to leave without a store gets one.
func TestScaffoldLedger_LegacyLocalOnlyEntryConverges(t *testing.T) {
	dir := scaffoldWorkloadModelProject(t)
	clean := readLedgerBytes(t, dir)

	var raw map[string]any
	if err := json.Unmarshal(clean, &raw); err != nil {
		t.Fatal(err)
	}
	raw["files"].(map[string]any)[localOnlyStore] = map[string]any{"scaffolded_at": "2026-09-04T15:19:42Z"}
	raw["absent_reported"] = []any{localOnlyStore}
	legacy, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, checksums.ScaffoldedFile), append(legacy, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	// The legacy shape that bit control-plane: entry committed, file absent.
	if err := os.RemoveAll(filepath.Join(dir, "secrets")); err != nil {
		t.Fatal(err)
	}

	regenerateEnvConfig(t, dir)

	if got := readLedgerBytes(t, dir); !bytes.Equal(got, clean) {
		t.Errorf("legacy entry was not dropped:\n--- want\n%s\n--- got\n%s", clean, got)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(localOnlyStore))); err != nil {
		t.Errorf("%s was not written for a clean checkout: %v", localOnlyStore, err)
	}
}
