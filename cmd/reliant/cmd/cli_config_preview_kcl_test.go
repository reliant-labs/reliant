// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestPreviewLoadsCLIConfigThroughKCL pins `reliant preview` as one of
// CLIConfig's subtrees on the path a workload takes, as
// TestProvidersLoadsCLIConfigThroughKCL does for providers: deploy/kcl's
// reliantProcessEnvMap (generated from cli_config.proto) projects the CLI
// connection into a `reliant preview` process's env, and projects CLIConfig's
// defaults into one declared without cli_config. Dropping preview from the
// subtrees fails both. The Cobra side is TestMachineLifecycleAndPreviewAccess.
func TestPreviewLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_preview.k", "preview_access_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "preview's env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	// No cli_config: CLIConfig has no required field, so it projects as its
	// defaults (forge #2659), exactly as an explicit `CLIConfig {}` does, and
	// still carries the CLI connection.
	const missing = "deploy/kcl/testdata/cli_config_preview_missing.k"
	var unset, defaults map[string]any
	require.NoError(t, kcleval.Select(context.Background(), missing, "missing_cli_config", &unset, project))
	require.NoError(t, kcleval.Select(context.Background(), missing, "default_cli_config", &defaults, project))
	require.Equal(t, defaults, unset, "a preview workload with no cli_config gets CLIConfig's defaults")
	require.NotEmpty(t, unset["RELIANT_SERVER_URL"], "the default projection carries the CLI connection: %v", unset)
}
