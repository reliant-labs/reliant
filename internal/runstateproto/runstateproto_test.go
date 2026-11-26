// Copyright (c) 2025 Reliant Labs
package runstateproto

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Every recorded outcome has its own proto value; one a newer release wrote
// reads unspecified, never as a success or a failure.
func TestOutcome(t *testing.T) {
	for outcome, want := range map[string]reliantv1.RunOutcome{
		core.RunOutcomeSucceeded:      reliantv1.RunOutcome_RUN_OUTCOME_SUCCEEDED,
		core.RunOutcomeFailed:         reliantv1.RunOutcome_RUN_OUTCOME_FAILED,
		core.RunOutcomeFailureHandled: reliantv1.RunOutcome_RUN_OUTCOME_FAILURE_HANDLED,
		core.RunOutcomeSkipped:        reliantv1.RunOutcome_RUN_OUTCOME_SKIPPED,
		"":                            reliantv1.RunOutcome_RUN_OUTCOME_UNSPECIFIED,
		"from_the_future":             reliantv1.RunOutcome_RUN_OUTCOME_UNSPECIFIED,
	} {
		if got := Outcome(outcome); got != want {
			t.Errorf("Outcome(%q) = %v, want %v", outcome, got, want)
		}
	}
}
