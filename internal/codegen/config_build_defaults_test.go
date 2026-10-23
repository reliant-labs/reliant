package codegen

import (
	"strings"
	"testing"
)

// The generated pkg/config declares the -ldflags injection site for the
// build-defaults layer and installs it for EVERY config the binary declares,
// so a stamp may name any of their env vars and a typo names none.
func TestConfigTemplate_InstallsBuildDefaultsForEveryConfig(t *testing.T) {
	got := renderConfigGo(t, commandScopedMessages())
	for _, want := range []string{
		"var BuildDefaults string",
		"forgeconfig.SetBuildDefaults(BuildDefaults, &configv1.ReliantConfig{}, &configv1.ApiServerConfig{}, &configv1.WorkerConfig{}, &configv1.MonolithConfig{})",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.go missing %q\n---\n%s", want, got)
		}
	}
}

// A subtree config's env vars are stampable too: it is one of the configs the
// binary declares.
func TestConfigTemplate_BuildDefaultsCoverSubtreeConfigs(t *testing.T) {
	got := renderConfigGo(t, subtreeKCLMessages())
	if !strings.Contains(got, "&configv1.CLIConfig{}") || !strings.Contains(got, "forgeconfig.SetBuildDefaults(BuildDefaults") {
		t.Errorf("SetBuildDefaults must include the subtree config CLIConfig:\n%s", got)
	}
}
