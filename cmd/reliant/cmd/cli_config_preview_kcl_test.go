// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestPreviewLoadsCLIConfigThroughKCL checks explicit CLI connection values and default
// projection when the optional CLIConfig instance is omitted.
func TestPreviewLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_preview.k", "preview_access_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "preview's env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var defaults map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_preview_missing.k", "missing_cli_config", &defaults, project))
	require.Equal(t, "https://api.reliantapi.com", defaults["RELIANT_SERVER_URL"])
	require.Equal(t, "https://gateway.reliantapi.com", defaults["RELIANT_GATEWAY_URL"])
	require.NotContains(t, defaults, "RELIANT_TOKEN", "omitting CLIConfig must not select a credential")
}
