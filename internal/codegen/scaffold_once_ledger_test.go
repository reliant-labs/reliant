package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// `forge generate` must not birth a scaffold-once command-tree file the
// project deleted (ledger) or disowned. reliant's metrics.go and commands.go
// were the report; this drives the generate-side writers that own them.
func TestGenerate_NeverRecreatesDeletedOrDisownedCmdTreeScaffolds(t *testing.T) {
	dir := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: proj\n")
	write("go.mod", "module example.com/proj\n\ngo 1.24\n")
	write("cmd/proj/cmd/root.go", "package cmd\n") // the tree anchor

	const (
		metrics  = "cmd/proj/cmd/metrics.go"
		commands = "cmd/proj/cmd/commands.go"
		pkg      = "internal/metrics/metrics.go"
	)
	checksums.RecordScaffold(dir, metrics)  // deleted by the user
	checksums.RecordScaffold(dir, commands) // deleted by the user
	write(".forge/disowned.json", `{"files":{"`+pkg+`":{"reason":"mine"}}}`)

	if err := GenerateCmdServerWithFields(map[string]bool{}, dir, nil); err != nil {
		t.Fatalf("GenerateCmdServerWithFields: %v", err)
	}
	if err := GenerateCmdCommands(dir, "proj"); err != nil {
		t.Fatalf("GenerateCmdCommands: %v", err)
	}

	for _, rel := range []string{metrics, commands, pkg} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("generate re-created %s, which the project deleted or disowned (stat err %v)", rel, err)
		}
	}
	// The rest of the tree is still born: the guard is per path.
	if _, err := os.Stat(filepath.Join(dir, "cmd/proj/cmd/version.go")); err != nil {
		t.Errorf("generate stopped birthing unrelated scaffolds: %v", err)
	}
}
