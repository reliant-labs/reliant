// Not t.Parallel: the birth ledger's in-memory cache and the CI discovery
// cache are process-global.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// PGO turns on by itself for an EXISTING hosted project (owner, 2026-11-20:
// "turn them on if there are profiles available"): the project was scaffolded
// before pgo-refresh.yml existed, and the next `forge generate` after the pin
// bump writes it beside the user's own CI files, which it leaves alone. A
// deleted one stays deleted, and `ci.workflows: {pgo-refresh: false}` declines
// it.
func TestGenerateCIWorkflows_ExistingHostedProjectGetsPGORefresh(t *testing.T) {
	const pgoRefresh = ".github/workflows/pgo-refresh.yml"
	setup := func(t *testing.T) (string, *config.ProjectConfig) {
		t.Helper()
		root := t.TempDir()
		checksums.ResetScaffoldLedgerCache()
		t.Cleanup(checksums.ResetScaffoldLedgerCache)
		cfg := &config.ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: config.ProjectKindService}
		config.ApplyDerivedDefaults(cfg)
		// Born by an older forge and edited since: the user's.
		const release = ".github/workflows/release.yml"
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, release), []byte("# yours: scaffolded once\nname: Release\n# edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		checksums.RecordScaffold(root, release)
		// prod is declared, and hosted (the render-backed discovery,
		// answered up front).
		if err := os.MkdirAll(filepath.Join(root, "deploy", "kcl", "prod"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "prod", "main.k"), []byte("# never evaluated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ciHostedMu.Lock()
		ciHostedCache[root] = ciHostedTopology{Hosted: []string{"prod"}}
		ciHostedMu.Unlock()
		t.Cleanup(func() {
			ciHostedMu.Lock()
			delete(ciHostedCache, root)
			ciHostedMu.Unlock()
		})
		return root, cfg
	}
	generate := func(t *testing.T, root string, cfg *config.ProjectConfig) {
		t.Helper()
		captureStdout(t, func() {
			if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
				t.Fatalf("generateCIWorkflows: %v", err)
			}
		})
	}

	t.Run("written on the next generate", func(t *testing.T) {
		root, cfg := setup(t)
		generate(t, root, cfg)
		b, err := os.ReadFile(filepath.Join(root, pgoRefresh))
		if err != nil {
			t.Fatalf("an existing hosted project did not get %s: %v", pgoRefresh, err)
		}
		if !strings.Contains(string(b), "forge pgo pull --env prod --json") {
			t.Errorf("%s does not pull prod:\n%s", pgoRefresh, b)
		}
		if got, _ := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml")); !strings.Contains(string(got), "# edited") {
			t.Error("generate rewrote the user's release.yml")
		}
	})

	t.Run("a deleted one stays deleted", func(t *testing.T) {
		root, cfg := setup(t)
		generate(t, root, cfg)
		if err := os.Remove(filepath.Join(root, pgoRefresh)); err != nil {
			t.Fatal(err)
		}
		generate(t, root, cfg)
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate re-created %s after the user deleted it", pgoRefresh)
		}
	})

	t.Run("declined in forge.yaml", func(t *testing.T) {
		root, cfg := setup(t)
		cfg.CI.Workflows = map[string]bool{"pgo-refresh": false}
		generate(t, root, cfg)
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate wrote %s although ci.workflows declines it", pgoRefresh)
		}
	})
}
