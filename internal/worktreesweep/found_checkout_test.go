package worktreesweep

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Only a result that found the checkout records an unattributed row's owner
// (and moves its chats' pins): a missing, foreign, unreached or failed one
// proves nothing about where the workspace lives.
func TestFoundCheckout(t *testing.T) {
	for outcome, want := range map[worktreereclaim.Outcome]bool{
		worktreereclaim.OutcomeLocked:            true,
		worktreereclaim.OutcomeRemoved:           true,
		worktreereclaim.OutcomeClaimed:           true,
		worktreereclaim.OutcomeIdleDone:          true,
		worktreereclaim.OutcomeProbeInconclusive: true,
		worktreereclaim.OutcomeKept:              true,
		worktreereclaim.OutcomeGone:              false,
		worktreereclaim.OutcomeForeign:           false,
		worktreereclaim.OutcomeDeferred:          false,
		worktreereclaim.OutcomeError:             false,
	} {
		assert.Equal(t, want, foundCheckout(worktreereclaim.Result{Outcome: outcome}), string(outcome))
	}
	assert.True(t, foundCheckout(worktreereclaim.Result{Outcome: worktreereclaim.OutcomeHeld, Reason: "dirty"}))
	assert.False(t, foundCheckout(worktreereclaim.Result{Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonError}),
		"a failed pass is reported as held with reason error")
}
