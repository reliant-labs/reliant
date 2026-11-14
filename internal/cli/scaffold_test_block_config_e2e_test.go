//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ETestHarnessBlockTypedConfig is the end-to-end test for a component
// that takes its own config block instead of the root config (the
// composition graph's K3): `Config *config.TraderConfig`.
//
// The harness used to recognise only `Config *<pkg>.Config`. A component
// converted to its block got a helpers_gen_test.go with no config default and
// no WithConfig, so NewTest<X> handed New a nil Config and the scaffolded
// validateDeps, which requires it, failed every test.
//
// The fixture composes a TraderConfig block onto AppConfig, converts the
// scaffolded ledger service to take it, drops ledger's Config key from
// compose.go, and runs the real generate. Then: pkg/config declares
// DefaultsTraderConfig, compose.go fills the key with the block by type, the
// project builds, and a test INSIDE the project constructs ledger through the
// harness and reads the block's defaults through WithConfig's type.
func TestE2ETestHarnessBlockTypedConfig(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "blockapp", "--mod", "example.com/blockapp", "--service", "ledger")
	projectDir := filepath.Join(dir, "blockapp")
	addCorpusForgePkgReplace(t, projectDir)

	cfgPath := filepath.Join(projectDir, "proto", "blockapp", "config", "v1", "config.proto")
	src := readFileE2E(t, cfgPath)
	const anchor = "  reserved 22;\n"
	if !strings.Contains(src, anchor) {
		t.Fatalf("config.proto has no %q to anchor the block field before:\n%s", anchor, src)
	}
	// Keep the fixture extension outside the scaffold's built-in tag range.
	src = strings.Replace(src, anchor, "  TraderConfig trader = 1000;\n\n"+anchor, 1)
	src += `
message TraderConfig {
  int32 max_per_tick = 1 [(forge.v1.config) = {
    env_var: "TRADER_MAX_PER_TICK"
    default_value: "10"
    description: "Maximum orders per tick"
  }];
  string api_key = 2 [(forge.v1.config) = {
    env_var: "TRADER_API_KEY"
    sensitive: true
    description: "Venue API key"
  }];
}
`
	writeFileE2E(t, cfgPath, src)

	// The ledger service takes its own block, not the root config.
	svcPath := filepath.Join(projectDir, "internal", "handlers", "ledger", "service.go")
	svc := readFileE2E(t, svcPath)
	const rootField = "\tConfig *config.Config\n"
	if !strings.Contains(svc, rootField) {
		t.Fatalf("the scaffolded ledger Deps has no %q to convert:\n%s", rootField, svc)
	}
	writeFileE2E(t, svcPath, strings.Replace(svc, rootField, "\tConfig *config.TraderConfig\n", 1))

	// compose.go is owned: the root config it hands ledger today is the
	// author's to change. Drop the key so generate resolves it afresh.
	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	compose := readFileE2E(t, composePath)
	const ledgerDeps = "\treturn ledger.New(ledger.Deps{\n\t\tLogger: g.Log,\n\t\tConfig: g.Cfg,\n\t})"
	if !strings.Contains(compose, ledgerDeps) {
		t.Fatalf("compose.go does not construct ledger as %q:\n%s", ledgerDeps, compose)
	}
	writeFileE2E(t, composePath, strings.Replace(compose, ledgerDeps,
		"\treturn ledger.New(ledger.Deps{\n\t\tLogger: g.Log,\n\t})", 1))

	runCmd(t, projectDir, forgeBin, "generate")

	if cfg := readFileE2E(t, filepath.Join(projectDir, "pkg", "config", "config_gen.go")); !strings.Contains(cfg, "func DefaultsTraderConfig() *TraderConfig {") {
		t.Fatalf("pkg/config must declare DefaultsTraderConfig beside the block's alias:\n%s", cfg)
	}
	if got := readFileE2E(t, composePath); !strings.Contains(got, "Config: g.Cfg.GetTrader(),") {
		t.Fatalf("compose.go must hand ledger its block by type:\n%s", got)
	}
	helpers := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "ledger", "helpers_gen_test.go"))
	for _, want := range []string{
		"func WithConfig(cfg *config.TraderConfig) TestOption",
		"cfg:    config.DefaultsTraderConfig(),",
	} {
		if !strings.Contains(helpers, want) {
			t.Fatalf("the ledger harness is missing %q:\n%s", want, helpers)
		}
	}

	probe := `package ledger

import (
	"testing"

	"example.com/blockapp/pkg/config"
)

func TestHarnessBlockConfig(t *testing.T) {
	// The harness reads nothing from the environment.
	t.Setenv("TRADER_MAX_PER_TICK", "99")

	// validateDeps requires Config: NewTestLedger fails the test if the
	// harness hands New a nil block.
	cfg := NewTestLedger(t).deps.Config
	if got := cfg.GetMaxPerTick(); got != 10 {
		t.Errorf("trader.max_per_tick = %d, want its default_value 10", got)
	}
	if got := cfg.GetApiKey(); got != "" {
		t.Errorf("trader.api_key (sensitive) = %q, want empty", got)
	}

	own := &config.TraderConfig{MaxPerTick: 3}
	if got := NewTestLedger(t, WithConfig(own)).deps.Config; got != own {
		t.Errorf("WithConfig: ledger got %p, want the block passed in (%p)", got, own)
	}
}
`
	writeFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "ledger", "harness_block_config_probe_test.go"), probe)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "TestHarnessBlockConfig", "./internal/handlers/ledger/")
}
