// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A worker built without a run-event store cannot record a run's outcome: the
// status activity says so (Temporal retries it) rather than dropping the
// terminal event.
func TestWorkflowStatusRefusesAMissingRunEventStore(t *testing.T) {
	a := NewWorkflowStatusActivity(WorkflowStatusDeps{Repo: &MockWorkflowStatusStore{}, Triggers: &MockStatusTriggers{}, Runs: &MockRunRecords{}, Workflows: &MockWorkflowRuns{}})
	_, err := a.Execute(context.Background(), WorkflowStatusInput{ChatID: "c", WorkflowID: "w", Status: "completed"})
	require.ErrorContains(t, err, "not configured")
}
