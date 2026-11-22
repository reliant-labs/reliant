// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestModelsLoadsCLIConfigThroughKCL pins `reliant models` as one of
// CLIConfig's subtrees on the path a workload takes: deploy/kcl's
// reliantProcessEnvMap (generated from cli_config.proto) projects the CLI
// connection into a `reliant models` process's env, projects CLIConfig's
// defaults into one declared without cli_config, and leaves a command outside
// every subtree without it. Dropping models from the subtrees fails the first
// and second cases; the Cobra side (the command loads the config it is given)
// is TestCLIGroupsConfigureTheMachine.
func TestModelsLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	const fixture = "deploy/kcl/testdata/cli_config_models.k"

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "models_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "models' env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var outside map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "outside_subtrees", &outside, project))
	_, has := outside["RELIANT_SERVER_URL"]
	require.False(t, has, "a command outside CLIConfig's subtrees carries none of it: %v", outside)

	// No cli_config: CLIConfig has no required field, so it projects as its
	// defaults (forge #2659), exactly as an explicit `CLIConfig {}` does, and
	// still carries the CLI connection.
	const missing = "deploy/kcl/testdata/cli_config_models_missing.k"
	var unset, defaults map[string]any
	require.NoError(t, kcleval.Select(context.Background(), missing, "missing_cli_config", &unset, project))
	require.NoError(t, kcleval.Select(context.Background(), missing, "default_cli_config", &defaults, project))
	require.Equal(t, defaults, unset, "a models workload with no cli_config gets CLIConfig's defaults")
	require.NotEmpty(t, unset["RELIANT_SERVER_URL"], "the default projection carries the CLI connection: %v", unset)
}
