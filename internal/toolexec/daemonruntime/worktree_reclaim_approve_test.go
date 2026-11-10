// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The approve command names the person it comes from and at least one
// approval; anything else is refused before anything is read or written,
// and nothing is started.
func TestReclaimApproveCommandRefusesAMalformedRequest(t *testing.T) {
	dir := useReclaimStateDir(t, needsApproval("p-1", "fp-1"))
	prev := startApprovalRun
	started := 0
	startApprovalRun = func(func()) { started++ }
	t.Cleanup(func() { startApprovalRun = prev })
	for _, payload := range []string{
		`{"approvals":[{"key":"p-1","fingerprint":"fp-1","path":"/tmp/p-1"}]}`,
		`{"approvals":[{"key":"p-1","fingerprint":"fp-1","path":"/tmp/p-1"}],"approved_by":"agent"}`,
		`{"approvals":[{"key":"p-1","fingerprint":"fp-1","path":"/tmp/p-1"}],"approved_by":"inbox:"}`,
		`{"approvals":[],"approved_by":"inbox:u1"}`,
		`not json`,
	} {
		if _, err := handleWorktreeReclaimApprove(context.Background(), []byte(payload)); err == nil {
			t.Fatalf("%s was accepted", payload)
		}
	}
	if started != 0 {
		t.Fatalf("%d removals started", started)
	}
	if results, _ := worktreereclaim.ApprovalCommandResults(dir); len(results) != 0 {
		t.Fatalf("results recorded: %+v", results)
	}
}

// On a machine whose reclaim is not on, an approval is recorded and refused
// at once: nothing is started.
func TestReclaimApproveCommandWithReclaimOffRemovesNothing(t *testing.T) {
	useReclaimStateDir(t, needsApproval("p-1", "fp-1"))
	setReclaimSweepConfig(worktreereclaim.SweepConfig{Mode: worktreereclaim.SweepDryRun})
	prev := startApprovalRun
	started := 0
	startApprovalRun = func(func()) { started++ }
	t.Cleanup(func() {
		startApprovalRun = prev
		setReclaimSweepConfig(worktreereclaim.SweepConfig{Mode: worktreereclaim.SweepOff})
	})
	raw, err := handleWorktreeReclaimApprove(context.Background(), []byte(`{"approvals":[{"key":"p-1","fingerprint":"fp-1","path":"/tmp/p-1"}],"approved_by":"inbox:u1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp worktreeReclaimApproveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	// /tmp/p-1 is not a linked worktree, which is refused before the mode.
	if len(resp.Results) != 1 || resp.Results[0].Status != worktreereclaim.ApprovalRefused || started != 0 {
		t.Fatalf("results %+v after %d starts, want refused and nothing started", resp.Results, started)
	}
	if r := resp.Results[0].Reason; r != worktreereclaim.RefusedNotLinked && !strings.Contains(r, "reclaim_off") {
		t.Fatalf("reason %q", r)
	}
}
