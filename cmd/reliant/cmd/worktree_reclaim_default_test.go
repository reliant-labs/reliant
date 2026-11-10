// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// TestWorktreeReclaimDefaultReachesEveryServerRoot pins the deployed default
// of the machine-wide worktree reclaim end to end: a ReliantConfig that leaves
// worktree_reclaim unset must project RELIANT_WORKTREE_RECLAIM=dry-run, plus
// its idle defaults, into every server root's env through
// config_gen.workloadEnvMap, and the root config loaded from that env must
// parse to a dry-run (report-only, never deleting) sweep. The unit tests of
// ParseSweepConfig take strings directly, so without this a schema default
// moved to "on", a dropped env-map entry, or a root that stopped reaching the
// variable would ship unseen.
func TestWorktreeReclaimDefaultReachesEveryServerRoot(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/worktree_reclaim_default.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantMode string
		want     worktreereclaim.SweepMode
	}{
		{"default_api", "dry-run", worktreereclaim.SweepDryRun},
		{"default_worker", "dry-run", worktreereclaim.SweepDryRun},
		{"default_gateway", "dry-run", worktreereclaim.SweepDryRun},
		{"mode_off", "off", worktreereclaim.SweepOff},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			for name, want := range map[string]string{
				"RELIANT_WORKTREE_RECLAIM":               tc.wantMode,
				"RELIANT_WORKTREE_RECLAIM_CLEAN_IDLE":    "24h",
				"RELIANT_WORKTREE_RECLAIM_APPROVAL_IDLE": "120h",
			} {
				got, ok := env[name]
				require.True(t, ok, "the projected env has no %s: %v", name, env)
				require.Equal(t, want, got, name)
			}

			// Load the root config from the variables as projected.
			isolateServerAPIConfigEnv(t)
			for _, name := range []string{"RELIANT_WORKTREE_RECLAIM", "RELIANT_WORKTREE_RECLAIM_ROOTS", "RELIANT_WORKTREE_RECLAIM_CLEAN_IDLE", "RELIANT_WORKTREE_RECLAIM_APPROVAL_IDLE"} {
				v, _ := env[name].(string)
				t.Setenv(name, v)
			}
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			cfg, err := loadRootConfig(cmd)
			require.NoError(t, err)
			sweep := worktreeReclaimConfig(cfg)
			require.Equal(t, tc.want, sweep.Mode)
			require.Equal(t, worktreereclaim.DefaultCleanIdle, sweep.CleanIdle)
			require.Equal(t, worktreereclaim.DefaultApprovalIdle, sweep.ApprovalIdle)
		})
	}
}
