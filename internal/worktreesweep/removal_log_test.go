// Copyright (c) 2026 Reliant Labs
package worktreesweep

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// A row the daemon claimed and then kept at removal is named with why; a
// removed or already-gone row is not.
func TestSummarizeRemovalNamesClaimedRowsThatWereKept(t *testing.T) {
	t.Parallel()
	sum := summarizeRemoval([]worktreereclaim.Result{
		{ID: "a", Outcome: worktreereclaim.OutcomeRemoved},
		{ID: "b", Outcome: worktreereclaim.OutcomeGone},
		{ID: "c", Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonInUse, Detail: "a running process has its working directory here"},
		{ID: "d", Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonDirty, Detail: "changed since it was checked"},
		{ID: "e", Outcome: worktreereclaim.OutcomeError, Error: "git failed"},
	})
	if sum.outcomes["removed"] != 1 || sum.outcomes["gone"] != 1 || sum.outcomes["held"] != 2 || sum.outcomes["error"] != 1 {
		t.Errorf("outcomes = %v", sum.outcomes)
	}
	if sum.held[string(worktreereclaim.ReasonInUse)] != 1 || sum.held[string(worktreereclaim.ReasonDirty)] != 1 {
		t.Errorf("held = %v", sum.held)
	}
	var kept []string
	for _, r := range sum.kept {
		kept = append(kept, r.ID)
	}
	if len(kept) != 3 || kept[0] != "c" || kept[1] != "d" || kept[2] != "e" {
		t.Errorf("kept = %v, want [c d e]", kept)
	}
	if sum.kept[0].Detail == "" {
		t.Error("a kept row loses its reason")
	}
}
