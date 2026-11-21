package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A run that ends at a node declaring outcome: failure returns no error, so
// its failed run event carried an empty trigger.payload.error (north-star
// AW-50). It names the node now; a real error and a finished run are
// unchanged.
func TestTerminalEventError(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `the run reached node "verdict", which declares outcome: failure`,
		terminalEventError(WorkflowStatusInput{OutcomeNode: "verdict"}, core.RunEventFailed))
	assert.Equal(t, "the run reached a node that declares outcome: failure",
		terminalEventError(WorkflowStatusInput{}, core.RunEventFailed))
	assert.Equal(t, "boom", terminalEventError(WorkflowStatusInput{Error: "boom", OutcomeNode: "verdict"}, core.RunEventFailed))
	assert.Empty(t, terminalEventError(WorkflowStatusInput{}, core.RunEventFinished))
}
