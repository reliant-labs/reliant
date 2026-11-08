// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// useReclaimStateDir points the reclaim's state at a fresh directory holding
// a report with the given needs-approval proposals. Not parallel: the state
// directory and the heartbeat cache are the daemon's, one per process.
func useReclaimStateDir(t *testing.T, proposals ...worktreereclaim.SweepEntry) string {
	t.Helper()
	dir := t.TempDir()
	prev := reclaimStateDir
	reclaimStateDir = func() (string, error) { return dir, nil }
	reclaimProposalsCache.invalidate()
	t.Cleanup(func() { reclaimStateDir = prev; reclaimProposalsCache.invalidate() })
	writeSweepReport(t, dir, proposals...)
	return dir
}

func writeSweepReport(t *testing.T, dir string, entries ...worktreereclaim.SweepEntry) {
	t.Helper()
	b, err := json.Marshal(worktreereclaim.SweepReport{Version: 1, Mode: worktreereclaim.SweepOn, FinishedAt: time.Now().UTC(), Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func needsApproval(key, fp string) worktreereclaim.SweepEntry {
	return worktreereclaim.SweepEntry{Key: key, Fingerprint: fp, Path: "/tmp/" + key, Repo: "/r", Branch: "feat/x", Owner: "unregistered",
		Class: worktreereclaim.ClassNeedsApproval, Outcome: worktreereclaim.OutcomeAwaiting, Reason: worktreereclaim.ReasonUnpushed,
		Detail: "HEAD is not on any remote branch\nmore", Work: "commits on no remote:\nabc wip", UnpushedCount: 1, IdleSeconds: 6 * 86400,
		Bytes: 1 << 20, ProposedAt: time.Now().Add(-time.Hour)}
}

// The heartbeat carries the pending proposals while the sweep is dry-run or
// on, and nothing when it is off.
func TestHeartbeatCarriesWorktreeReclaimProposals(t *testing.T) {
	useReclaimStateDir(t, needsApproval("p-1", "fp-1"))
	if got := worktreeReclaimProposals(worktreereclaim.SweepOff); got != nil {
		t.Fatalf("mode off carried %+v", got)
	}
	got := worktreeReclaimProposals(worktreereclaim.SweepDryRun)
	if got == nil || got.Total != 1 || got.Mode != "on" || len(got.Proposals) != 1 {
		t.Fatalf("proposals = %+v", got)
	}
	p := got.Proposals[0]
	if p.Key != "p-1" || p.Fingerprint != "fp-1" || p.Branch != "feat/x" || p.Reason != "unpushed" || p.Detail != "HEAD is not on any remote branch" ||
		p.UnpushedCommits != 1 || p.SizeBytes != 1<<20 || p.ProposedAtMs == 0 || p.Decision != "" {
		t.Fatalf("proposal = %+v", p)
	}
}

// worktree.reclaim_decide records the person's decision exactly as the CLI
// does, bound to the fingerprint shown; the next heartbeat says so.
func TestReclaimDecideCommandRecordsTheDecisionShown(t *testing.T) {
	dir := useReclaimStateDir(t, needsApproval("p-1", "fp-1"), needsApproval("p-2", "fp-2"))
	payload, _ := json.Marshal(worktreeReclaimDecideRequest{DecidedBy: "inbox:user-1", Decisions: []worktreereclaim.DecisionRequest{
		{Key: "p-1", Fingerprint: "fp-1", Decision: worktreereclaim.DecisionApprove},
		{Key: "p-2", Fingerprint: "fp-OLD", Decision: worktreereclaim.DecisionApprove},
	}})
	if got := worktreeReclaimProposals(worktreereclaim.SweepOn); got.Proposals[0].Decision != "" {
		t.Fatalf("decided before the command: %+v", got.Proposals[0])
	}
	raw, err := handleWorktreeReclaimDecide(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var resp worktreeReclaimDecideResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 || !resp.Results[0].Recorded || resp.Results[1].Recorded || resp.Results[1].Reason != "changed" {
		t.Fatalf("results = %+v", resp.Results)
	}
	if !worktreereclaim.PendingApprovals(dir, time.Time{}) {
		t.Fatal("no approval pending for the sweep to act on")
	}
	got := worktreeReclaimProposals(worktreereclaim.SweepOn)
	if got.Proposals[0].Decision != "approve" || got.Proposals[1].Decision != "" {
		t.Fatalf("after deciding: %+v", got.Proposals)
	}
	if _, err := handleWorktreeReclaimDecide(context.Background(), []byte(`{"decisions":[]}`)); err == nil {
		t.Fatal("an empty decision list was accepted")
	}
}
