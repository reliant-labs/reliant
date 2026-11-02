package tools

import (
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
)

// After approval, a failed change keeps its reason: the refusal's safe
// classification or a classified error's client message. An internal error's
// cause never reaches the agent.
func TestActivationFailureMessageKeepsTheReason(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains string
		absent   string
	}{
		{"refusal", RefuseActivation("workflow does not declare that trigger", svcerr.NotFound("declared trigger")), "not completed: workflow does not declare that trigger.", ""},
		{"classified", svcerr.FailedPrecondition("The activation or workflow changed after approval. Request a new approval; nothing was changed."), "not completed: The activation or workflow changed after approval", ""},
		{"internal", errors.New("pq: relation \"triggers\" does not exist"), "could not be completed", "pq:"},
		{"wrapped internal", svcerr.Wrap(errors.New("dial tcp 10.0.0.1:5432")), "could not be completed", "dial tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := activationFailureMessage(ToolCreateTriggerActivation, tc.err)
			require.Contains(t, got, tc.contains)
			if tc.absent != "" {
				require.NotContains(t, got, tc.absent)
			}
			require.NotContains(t, got, "..", "a reason's own full stop is not doubled")
		})
	}
}

// A pause never needs a card, so a failed one keeps its reason and never tells
// the agent a new approval is needed.
func TestPauseFailureMessageNeverAsksForApproval(t *testing.T) {
	for _, err := range []error{
		RefuseActivation("workflow does not declare that trigger", svcerr.NotFound("declared trigger")),
		errors.New("pq: commit unexpectedly resulted in rollback"),
	} {
		got := activationFailureMessage(ToolPauseTrigger, err)
		require.Contains(t, got, "Pausing the activation")
		require.NotContains(t, got, "approval")
		require.NotContains(t, got, "pq:")
	}
	require.Contains(t, activationFailureMessage(ToolPauseTrigger, RefuseActivation("workflow does not declare that trigger", svcerr.NotFound("declared trigger"))), "not completed: workflow does not declare that trigger.")
}
