// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func strandedRoot(id string) *db.Workflow {
	return &db.Workflow{
		ID: id, ChatID: id, WorkflowName: "builtin://agent", Thread: id,
		Status: db.Pending(), CreatedAt: time.Now().Add(-time.Hour),
	}
}

// A committed launch whose run never reached Temporal, with no failed start
// recorded (its starter died between the commit and the start), is recorded
// as a failed start: that is what stops a trigger's next fire waiting behind
// it forever.
func TestReconcileRecordsAStrandedStartTemporalNeverSaw(t *testing.T) {
	repo := newMockRepo()
	repo.strandedStarts = []*db.Workflow{strandedRoot("chat-stranded")}
	before := anomalyCount(anomalyStrandedStartFailed)

	asked := time.Now()
	reconciled, errs := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig()).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Equal(t, []string{"chat-stranded"}, repo.markedStartFailed)
	assert.Empty(t, repo.updatedStatuses, "the row stays PENDING so a retry of its launch can still start it")
	assert.Equal(t, 1, reconciled)
	assert.Equal(t, before+1, anomalyCount(anomalyStrandedStartFailed))
	assert.WithinDuration(t, asked.Add(-strandedStartBound), repo.strandedStartsCutoff, 5*time.Second,
		"only launches older than the bound may be judged stranded")
}

// Temporal has the run: the start happened and only the launch's mark-active
// was lost. That root is running, so it moves to ACTIVE, never to a failed
// start a trigger's next fire would abandon.
func TestReconcileMarksAStrandedStartActiveWhenTemporalHasTheRun(t *testing.T) {
	repo := newMockRepo()
	repo.strandedStarts = []*db.Workflow{strandedRoot("chat-started")}
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"chat-started": {resp: makeRunningDescribeResp("run-1")},
	}}
	before := anomalyCount(anomalyStrandedStartRepaired)

	_, errs := NewReconciler(repo, tempClient, DefaultConfig()).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Empty(t, repo.markedStartFailed)
	assert.Equal(t, db.Active(), repo.updatedStatuses["chat-started"])
	assert.Equal(t, before+1, anomalyCount(anomalyStrandedStartRepaired))
}

// When Temporal cannot be asked, nothing is decided: the next pass asks again.
func TestReconcileLeavesAStrandedStartAloneWhenTemporalCannotBeAsked(t *testing.T) {
	repo := newMockRepo()
	repo.strandedStarts = []*db.Workflow{strandedRoot("chat-unknown")}
	tempClient := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"chat-unknown": {err: errors.New("connection refused")},
	}}

	_, errs := NewReconciler(repo, tempClient, DefaultConfig()).ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Empty(t, repo.markedStartFailed)
	assert.Empty(t, repo.updatedStatuses)
}
