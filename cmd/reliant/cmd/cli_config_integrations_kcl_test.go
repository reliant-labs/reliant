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

// TestIntegrationsLoadsCLIConfigThroughKCL pins `reliant integrations` as one of CLIConfig's
// subtrees on the path a workload takes, as TestProvidersLoadsCLIConfigThroughKCL
// does for providers: deploy/kcl's reliantProcessEnvMap (generated from
// cli_config.proto) projects the CLI connection into a `reliant integrations`
// process's env, and refuses one declared without cli_config. Dropping integrations
// from the subtrees fails both. The Cobra side is
// TestIntegrationsListDialsTheServer.
func TestIntegrationsLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_integrations.k", "integrations_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "integrations' env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	var refused map[string]any
	err := kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_integrations_missing.k", "missing_cli_config", &refused, project)
	require.Error(t, err, "an integrations workload with no cli_config must be refused")
	require.True(t, strings.Contains(err.Error(), "reliant integrations") && strings.Contains(err.Error(), "cli_config"),
		"the refusal names the command and the missing config: %v", err)
}
