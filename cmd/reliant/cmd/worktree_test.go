// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// worktreeReclaimFor loads the root config the way `daemon start` does and
// returns the worktree reclaim it hands the daemon runtime.
func worktreeReclaimFor(t *testing.T, env map[string]string) worktreereclaim.SweepConfig {
	t.Helper()
	for _, k := range []string{"RELIANT_WORKTREE_RECLAIM", "RELIANT_WORKTREE_RECLAIM_ROOTS", "RELIANT_WORKTREE_RECLAIM_CLEAN_IDLE", "RELIANT_WORKTREE_RECLAIM_APPROVAL_IDLE"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	runenv.ConfigureForTest(t, "")
	cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(nil))
	cfg, err := loadRootConfig(cmd)
	require.NoError(t, err)
	return worktreeReclaimConfig(cfg)
}

// Dry-run is the default for one release (owner decision, 2026-11-08); a
// machine opts in to on, or out to off, by its config.
func TestWorktreeReclaimConfig_DefaultsToDryRun(t *testing.T) {
	def := worktreeReclaimFor(t, nil)
	assert.Equal(t, worktreereclaim.SweepDryRun, def.Mode)
	assert.Equal(t, 24*time.Hour, def.CleanIdle)
	assert.Equal(t, 120*time.Hour, def.ApprovalIdle)
	assert.Nil(t, def.Roots, "unset: the built-in roots")

	on := worktreeReclaimFor(t, map[string]string{
		"RELIANT_WORKTREE_RECLAIM":               "on",
		"RELIANT_WORKTREE_RECLAIM_ROOTS":         "/tmp:2,/srv/wt",
		"RELIANT_WORKTREE_RECLAIM_CLEAN_IDLE":    "48h",
		"RELIANT_WORKTREE_RECLAIM_APPROVAL_IDLE": "30m",
	})
	assert.Equal(t, worktreereclaim.SweepOn, on.Mode)
	assert.Equal(t, []worktreereclaim.SweepRoot{{Path: "/tmp", Depth: 2}, {Path: "/srv/wt", Depth: 3}}, on.Roots)
	assert.Equal(t, 48*time.Hour, on.CleanIdle)
	assert.Equal(t, 24*time.Hour, on.ApprovalIdle, "below the minimum is raised to it")

	assert.Equal(t, worktreereclaim.SweepOff, worktreeReclaimFor(t, map[string]string{"RELIANT_WORKTREE_RECLAIM": "off"}).Mode)
}

// Approving a reclaim is a person's decision: an agent's shell is refused
// unless it names the user's answer that authorized it.
func TestWorktreeApproveRefusesAnAgentShellWithoutTheUsersAnswer(t *testing.T) {
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"worktree", "reclaim", "approve", "p-0123456789abcdef", "--state-dir", t.TempDir()})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "an agent cannot approve")

	// With an answer it gets as far as the report, which does not exist here.
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"worktree", "reclaim", "approve", "p-0123456789abcdef", "--answer", "q-1", "--state-dir", t.TempDir()})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sweep report")
}
