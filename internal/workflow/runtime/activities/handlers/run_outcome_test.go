// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A run's outcome is judged as it ends (reliant#2753): a run that completed
// past a node whose last attempt failed handled a failure, and is not a
// success; a failed run, or one that completed into a declared failure, failed.
func TestResolveRunOutcome(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var asked []time.Time
	var askedRuns []string
	handled := &db.HandledNodeFailure{Node: "post_failed", Error: "channel_not_found"}
	store := &MockWorkflowStatusStore{}
	storeRuns := &MockRunRecords{
		FirstHandledNodeFailureFunc: func(_ context.Context, workflowID, runID string, since time.Time) (*db.HandledNodeFailure, error) {
			asked = append(asked, since)
			askedRuns = append(askedRuns, runID)
			switch workflowID {
			case "wf-handled":
				return handled, nil
			case "wf-unreadable":
				return nil, errors.New("connection reset")
			}
			return nil, nil
		},
	}
	a := &WorkflowStatusActivity{repo: store, runs: storeRuns}
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		input WorkflowStatusInput
		want  string
	}{
		{"failed", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "failed"}, core.RunOutcomeFailed},
		{"declared failure", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "completed", Outcome: model.OutcomeFailure}, core.RunOutcomeFailed},
		{"declared skip", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "completed", Outcome: model.OutcomeSkipped}, core.RunOutcomeSkipped},
		{"a declared skip wins over a handled failure", WorkflowStatusInput{WorkflowID: "wf-handled", Status: "completed", Outcome: model.OutcomeSkipped}, core.RunOutcomeSkipped},
		{"clean completion", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "completed", RunStartedAtMs: started.UnixMilli(), RunID: "run-1"}, core.RunOutcomeSucceeded},
		{"completed past a failed node", WorkflowStatusInput{WorkflowID: "wf-handled", Status: "completed", RunStartedAtMs: started.UnixMilli()}, core.RunOutcomeFailureHandled},
		{"unreadable attempts keep the old reading", WorkflowStatusInput{WorkflowID: "wf-unreadable", Status: "completed"}, core.RunOutcomeSucceeded},
		{"cancelled has none", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "cancelled"}, ""},
		{"started has none", WorkflowStatusInput{WorkflowID: "wf-clean", Status: "started"}, ""},
	} {
		got := a.resolveRunOutcome(ctx, tc.input)
		assert.Equal(t, tc.want, got.outcome, tc.name)
		if tc.want == core.RunOutcomeFailureHandled {
			assert.Same(t, handled, got.handled, tc.name)
		} else {
			assert.Nil(t, got.handled, tc.name)
		}
	}

	// Only a completion reads the attempts: the run's own, by its id, and
	// for a run with no id, from the run's start (less the clock skew
	// allowance); with no start, every attempt.
	require.Len(t, asked, 3)
	assert.Equal(t, []string{"run-1", "", ""}, askedRuns, "the run's id is what its attempts are read by")
	assert.True(t, started.Add(-runStartSkew).Equal(asked[0]), "since is the start less the skew allowance: %v", asked[0])
	assert.True(t, asked[2].IsZero())

	assert.Equal(t, `the run completed, but node "post_failed" failed and its failure path ran: channel_not_found`, handledFailureDetail(handled))
	assert.Empty(t, handledFailureDetail(nil))
}

// The outcome lands on the run row in the terminal status's transaction, and
// on the launch event beside its run_status, with the handled node as the
// firing's detail.
func TestWorkflowStatus_RecordsTheRunOutcome(t *testing.T) {
	t.Parallel()

	var rowOutcome, eventOutcome, eventDetail string
	store := &MockWorkflowStatusStore{}
	storeRuns := &MockRunRecords{
		FirstHandledNodeFailureFunc: func(context.Context, string, string, time.Time) (*db.HandledNodeFailure, error) {
			return &db.HandledNodeFailure{Node: "post_failed", Error: "channel_not_found"}, nil
		},
		SetWorkflowRunOutcomeFunc: func(_ context.Context, _ string, outcome string) error {
			rowOutcome = outcome
			return nil
		},
		SetLaunchEventRunOutcomeFunc: func(_ context.Context, _ string, outcome string) error {
			eventOutcome = outcome
			return nil
		},
		SetLaunchEventRunFailureDetailFunc: func(_ context.Context, _ string, detail string) error {
			eventDetail = detail
			return nil
		},
		SetLaunchEventRunStatusFunc: func(context.Context, string, string) error { return nil },
	}
	a := &WorkflowStatusActivity{repo: store, runs: storeRuns}
	input := WorkflowStatusInput{WorkflowID: "wf", ChatID: "chat", Status: "completed", LaunchRun: true}
	input.resolved = a.resolveRunOutcome(context.Background(), input)

	require.NoError(t, a.setRunOutcome(context.Background(), input))
	a.recordLaunchRunStatus(context.Background(), input)

	assert.Equal(t, core.RunOutcomeFailureHandled, rowOutcome)
	assert.Equal(t, core.RunOutcomeFailureHandled, eventOutcome)
	assert.Contains(t, eventDetail, "post_failed")
}
