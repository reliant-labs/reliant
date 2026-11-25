// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestProvidersLoadsCLIConfigThroughKCL checks explicit CLI connection values and default
// projection when the optional CLIConfig instance is omitted.
func TestProvidersLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	const fixture = "deploy/kcl/testdata/cli_config_providers.k"

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "providers_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "providers' env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var outside map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "outside_subtrees", &outside, project))
	_, has := outside["RELIANT_SERVER_URL"]
	require.False(t, has, "a command outside CLIConfig's subtrees carries none of it: %v", outside)

	var defaults map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_providers_missing.k", "missing_cli_config", &defaults, project))
	require.Equal(t, "https://api.reliantapi.com", defaults["RELIANT_SERVER_URL"])
	require.Equal(t, "https://gateway.reliantapi.com", defaults["RELIANT_GATEWAY_URL"])
	require.NotContains(t, defaults, "RELIANT_TOKEN", "omitting CLIConfig must not select a credential")
}
