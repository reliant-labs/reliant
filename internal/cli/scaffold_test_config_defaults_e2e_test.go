//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ETestHarnessConfigIsProductionDefaults is the end-to-end regression
// test for #749: the generated test harness hands components the config
// production loads with nothing set, not an empty message.
//
// defaultTestConfig (helpers_gen_test.go) used to return &config.Config{}.
// A nested config block was therefore nil, and every defaulted leaf read
// through its nil-safe getter as zero — a project's hard-rule limits all
// read 0 in its handler tests, a config no production process ever has.
//
// The fixture composes a TraderConfig block with default_value leaves and a
// sensitive leaf onto AppConfig, runs the real generate, and then runs a test
// INSIDE the generated project that reads the harness's config: the block is
// allocated, each leaf carries its default, the sensitive leaf is empty, the
// root's own defaults hold, and an exported env var does not leak in.
func TestE2ETestHarnessConfigIsProductionDefaults(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "cfgapp", "--mod", "example.com/cfgapp", "--service", "ledger")
	projectDir := filepath.Join(dir, "cfgapp")
	addCorpusForgePkgReplace(t, projectDir)

	cfgPath := filepath.Join(projectDir, "proto", "cfgapp", "config", "v1", "config.proto")
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
  string venue = 2 [(forge.v1.config) = {
    env_var: "TRADER_VENUE"
    default_value: "paper"
    description: "Execution venue"
  }];
  string api_key = 3 [(forge.v1.config) = {
    env_var: "TRADER_API_KEY"
    sensitive: true
    description: "Venue API key"
  }];
}
`
	if err := os.WriteFile(cfgPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write config.proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "generate")

	helpers := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "ledger", "helpers_gen_test.go"))
	if !strings.Contains(helpers, "config.Defaults()") {
		t.Fatalf("defaultTestConfig must build the config with config.Defaults():\n%s", helpers)
	}

	probe := `package ledger

import (
	"testing"
	"time"
)

func TestHarnessConfigIsProductionDefaults(t *testing.T) {
	// The harness reads nothing from the environment: a developer's shell
	// must not change what a handler test sees.
	t.Setenv("TRADER_MAX_PER_TICK", "99")

	cfg := defaultTestConfig(t).cfg
	if cfg.GetTrader() == nil {
		t.Fatal("cfg.Trader is nil: the nested config block was not allocated")
	}
	if got := cfg.GetTrader().GetMaxPerTick(); got != 10 {
		t.Errorf("trader.max_per_tick = %d, want its default_value 10", got)
	}
	if got := cfg.GetTrader().GetVenue(); got != "paper" {
		t.Errorf("trader.venue = %q, want its default_value paper", got)
	}
	if got := cfg.GetTrader().GetApiKey(); got != "" {
		t.Errorf("trader.api_key (sensitive) = %q, want empty", got)
	}
	if got := cfg.GetPort(); got != 8080 {
		t.Errorf("port = %d, want its default_value 8080", got)
	}
	if got := cfg.GetShutdownTimeout().AsDuration(); got != 30*time.Second {
		t.Errorf("shutdown_timeout = %v, want its default_value 30s", got)
	}
}
`
	probePath := filepath.Join(projectDir, "internal", "handlers", "ledger", "harness_config_probe_test.go")
	if err := os.WriteFile(probePath, []byte(probe), 0o644); err != nil {
		t.Fatalf("write probe test: %v", err)
	}
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "TestHarnessConfigIsProductionDefaults", "./internal/handlers/ledger/")
}
