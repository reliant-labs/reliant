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

// TestPreviewLoadsCLIConfigThroughKCL pins `reliant preview` as one of CLIConfig's
// subtrees on the path a workload takes, as TestProvidersLoadsCLIConfigThroughKCL
// does for providers: deploy/kcl's reliantProcessEnvMap (generated from
// cli_config.proto) projects the CLI connection into a `reliant preview`
// process's env, and refuses one declared without cli_config. Dropping preview
// from the subtrees fails both. The Cobra side is
// TestMachineLifecycleAndPreviewAccess.
func TestPreviewLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_preview.k", "preview_access_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "preview's env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var refused map[string]any
	err := kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_preview_missing.k", "missing_cli_config", &refused, project)
	require.Error(t, err, "a preview workload with no cli_config must be refused")
	require.True(t, strings.Contains(err.Error(), "reliant preview") && strings.Contains(err.Error(), "cli_config"),
		"the refusal names the command and the missing config: %v", err)
}
