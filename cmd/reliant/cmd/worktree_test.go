// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// A reviewing agent lists the proposals and attaches notes in one call each;
// a note is not a decision, so an agent's shell may write one.
func TestWorktreeProposalsAndNotes(t *testing.T) {
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	dir := t.TempDir()
	rep := worktreereclaim.SweepReport{Version: 1, Mode: worktreereclaim.SweepOn, FinishedAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), Entries: []worktreereclaim.SweepEntry{{
		Key: "p-1", Fingerprint: "fp-1", Path: "/tmp/wt", Class: worktreereclaim.ClassNeedsApproval, Outcome: worktreereclaim.OutcomeAwaiting,
	}}}
	b, _ := json.Marshal(rep)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600))
	run := func(stdin string, args ...string) (string, error) {
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(append(args, "--state-dir", dir))
		err := root.Execute()
		return out.String(), err
	}
	out, err := run("", "worktree", "reclaim", "proposals")
	require.NoError(t, err)
	var set worktreereclaim.ProposalSet
	require.NoError(t, json.Unmarshal([]byte(out), &set))
	require.Len(t, set.Proposals, 1)
	assert.Equal(t, "fp-1", set.Proposals[0].Fingerprint)

	out, err = run(`[{"key":"p-1","fingerprint":"fp-1","text":"looks abandoned"}]`, "worktree", "reclaim", "note", "--from", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "noted p-1")
	_, err = run("", "worktree", "reclaim", "note", "p-1", "stale", "--fingerprint", "fp-0")
	require.Error(t, err)

	// --from is bounded: an oversized input is refused before it is parsed,
	// and the note already recorded stands.
	huge := `[{"key":"p-1","fingerprint":"fp-1","text":"` + strings.Repeat("x", worktreereclaim.MaxNotesInputBytes) + `"}]`
	_, err = run(huge, "worktree", "reclaim", "note", "--from", "-")
	require.ErrorContains(t, err, "more than")

	set, err = worktreereclaim.PendingProposals(dir)
	require.NoError(t, err)
	assert.Equal(t, "looks abandoned", set.Proposals[0].Note)
}

// Approving a reclaim is refused to everyone: no check this command could
// make tells a person from an agent running as the same user, so an agent
// that clears the agent-shell marker is refused exactly as one that keeps
// it, and nothing is recorded. Declining (which only ever keeps a worktree)
// is refused to an agent's shell.
func TestWorktreeApproveIsRefusedEvenWithoutTheAgentMarker(t *testing.T) {
	decide := func(t *testing.T, args ...string) ([]os.DirEntry, error) {
		t.Helper()
		state := t.TempDir()
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append(append([]string{"worktree", "reclaim"}, args...), "--state-dir", state))
		err := root.Execute()
		entries, rerr := os.ReadDir(state)
		require.NoError(t, rerr)
		return entries, err
	}
	for _, marker := range []string{"1", ""} {
		t.Setenv(daemonpolicy.AgentShellEnv, marker)
		for _, extra := range [][]string{nil, {"--answer", "q-1"}} {
			entries, err := decide(t, append([]string{"approve", "p-0123456789abcdef"}, extra...)...)
			require.Error(t, err, "marker %q %v", marker, extra)
			if extra == nil {
				assert.Contains(t, err.Error(), "approving a reclaim here is not available")
			}
			assert.Empty(t, entries, "a refused approval wrote state")
		}
	}

	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	entries, err := decide(t, "decline", "p-0123456789abcdef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "an agent cannot decline")
	assert.Empty(t, entries, "a refused decision wrote state")

	// A person's shell gets as far as the report, which does not exist here.
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	_, err = decide(t, "decline", "p-0123456789abcdef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sweep report")
}

// The report leads with every process that blocks reclaim, one line each, so
// whoever reads it knows what to end.
func TestPrintSweepReport_NamesBlockers(t *testing.T) {
	seen := time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC)
	rep := worktreereclaim.SweepReport{Mode: worktreereclaim.SweepDryRun, FinishedAt: seen, Blockers: []worktreereclaim.ReclaimBlocker{
		{PID: 2789376, Comm: "python3", Unreadable: "cwd", StartedAt: seen.Add(-7 * time.Hour), FirstSeen: seen, LastSeen: seen, Worktrees: 39},
	}}
	var out bytes.Buffer
	printSweepReport(&out, rep)
	assert.Contains(t, out.String(), "BLOCKED: pid 2789376 (python3, up 7h0m0s) blocks worktree reclaim: 39 worktree(s) held; its cwd cannot be read")
}
