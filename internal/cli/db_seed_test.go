package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The seed-apply dev gate is FAIL-CLOSED and classifies by the runtime MODE in
// the per-env KCL config (deploy/kcl/<env>/config.k) — only "development"/"dev"
// is allowed; everything else (production, an unset mode, a missing file) is
// refused.
func TestSeedEnvClassification(t *testing.T) {
	dir := t.TempDir()
	writeConfigK := func(env, body string) {
		envDir := filepath.Join(dir, "deploy", "kcl", env)
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(envDir, "config.k"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const kHeader = "import config_gen\n\napp_config: config_gen.AppConfig = {\n"

	// dev: config.k marks development (the scaffolded shape) -> dev.
	writeConfigK("dev", kHeader+"    environment = \"development\"\n}\n")
	// prod: config.k marks production -> not dev.
	writeConfigK("prod", kHeader+"    environment = \"production\"\n}\n")
	// staging: config.k present but sets no environment (inherits the schema
	// default, production) -> not dev, fail-closed.
	writeConfigK("staging", kHeader+"}\n")

	cases := []struct {
		env  string
		want bool
	}{
		{"dev", true},
		{"prod", false},
		{"staging", false}, // mode unset -> not dev
		{"nope", false},    // missing config.k -> fail closed
	}
	for _, tc := range cases {
		if got := seedEnvIsDevIn(dir, tc.env); got != tc.want {
			t.Errorf("seedEnvIsDevIn(%q) = %v, want %v", tc.env, got, tc.want)
		}
	}
}

// The seed config is the library default: forge.yaml has no database.seed
// block, so nothing project-specific can reach the planner.
func TestSeedConfigIsTheLibraryDefault(t *testing.T) {
	got := seedConfigFromProject()
	want := seedplan.DefaultConfig()
	if got.Rows != want.Rows || got.Salt != want.Salt || got.Tables != nil {
		t.Errorf("seed config = %+v, want the library default %+v", got, want)
	}
	if got.Rows != 20 {
		t.Errorf("default rows = %d, want 20 (fills a page and exercises pagination)", got.Rows)
	}
}

// The project seed config renders committed files (`forge generate`'s frontend
// mocks), so it must not read the clock; only liveSeedConfig — the one the
// commands writing a dev database use — anchors at today.
func TestSeedConfigReadsTheClockOnlyWhenSeedingALiveDatabase(t *testing.T) {
	prev := seedClock
	t.Cleanup(func() { seedClock = prev })

	for _, now := range []time.Time{
		time.Date(2026, 10, 11, 23, 59, 0, 0, time.UTC),
		time.Date(2026, 10, 12, 0, 1, 0, 0, time.UTC),
	} {
		seedClock = func() time.Time { return now }
		if got := seedConfigFromProject().Now; !got.IsZero() {
			t.Errorf("at %s seedConfigFromProject().Now = %s, want unset — generated mocks would go stale at midnight UTC",
				now.Format(time.RFC3339), got.Format(time.RFC3339))
		}
		want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if got := liveSeedConfig().Now; !got.Equal(want) {
			t.Errorf("at %s liveSeedConfig().Now = %s, want the start of that UTC day %s",
				now.Format(time.RFC3339), got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

// There is deliberately NO override flag on apply/reset — an override is
// exactly the conventional hole the structural gate exists to close.
func TestSeedApplyHasNoOverrideFlag(t *testing.T) {
	cmd := newDBSeedApplyCommand()
	banned := []string{"allow-nondev", "allow-non-dev", "force", "prod", "unsafe", "yes"}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		for _, b := range banned {
			if f.Name == b {
				t.Errorf("apply must not expose an override flag, found --%s", f.Name)
			}
		}
	})
}
