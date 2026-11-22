// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestWorktreeLoadsCLIConfigThroughKCL pins `reliant worktree` as one of
// CLIConfig's subtrees on the path a workload takes, as
// TestChatLoadsCLIConfigThroughKCL does for chat: deploy/kcl's
// reliantProcessEnvMap (generated from cli_config.proto) projects the CLI
// connection into a `reliant worktree` process's env, and projects CLIConfig's
// defaults into one declared without cli_config. Dropping worktree from the
// subtrees fails both. The Cobra side is
// TestWorktreeListAndArchiveDialTheServer.
func TestWorktreeLoadsCLIConfigThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/cli_config_worktree.k", "worktree_list", &env, project))
	require.Equal(t, "https://api.example.test", env["RELIANT_SERVER_URL"], "worktree's env carries CLIConfig: %v", env)
	require.Equal(t, "https://gateway.example.test", env["RELIANT_GATEWAY_URL"])

	// No cli_config: CLIConfig has no required field, so it projects as its
	// defaults (forge #2659), exactly as an explicit `CLIConfig {}` does, and
	// still carries the CLI connection.
	const missing = "deploy/kcl/testdata/cli_config_worktree_missing.k"
	var unset, defaults map[string]any
	require.NoError(t, kcleval.Select(context.Background(), missing, "missing_cli_config", &unset, project))
	require.NoError(t, kcleval.Select(context.Background(), missing, "default_cli_config", &defaults, project))
	require.Equal(t, defaults, unset, "a worktree workload with no cli_config gets CLIConfig's defaults")
	require.NotEmpty(t, unset["RELIANT_SERVER_URL"], "the default projection carries the CLI connection: %v", unset)
}
