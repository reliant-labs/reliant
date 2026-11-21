// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestMCPLoadsCLIConfigThroughKCL pins `reliant mcp` as one of CLIConfig's
// subtrees on the path a workload takes: deploy/kcl's reliantProcessEnvMap
// (generated from cli_config.proto) projects the CLI connection into a
// `reliant mcp` process's env, refuses one declared without cli_config, and
// leaves a command outside every subtree without it. Dropping mcp from the
// subtrees fails the first and second cases; the Cobra side (the command loads
// the config it is given) is TestCLIGroupsConfigureTheMachine.
func TestMCPLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	const fixture = "deploy/kcl/testdata/cli_config_mcp.k"

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "mcp_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "mcp's env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var outside map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "outside_subtrees", &outside, project))
	_, has := outside["RELIANT_SERVER_URL"]
	require.False(t, has, "a command outside CLIConfig's subtrees carries none of it: %v", outside)

	var refused map[string]any
	err := kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_mcp_missing.k", "missing_cli_config", &refused, project)
	require.Error(t, err, "an mcp workload with no cli_config must be refused")
	require.True(t, strings.Contains(err.Error(), "reliant mcp") && strings.Contains(err.Error(), "cli_config"),
		"the refusal names the command and the missing config: %v", err)
}
