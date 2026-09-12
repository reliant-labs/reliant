// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strptr(s string) *string { return &s }
func i64ptr(i int64) *int64   { return &i }

// TestRunToProto_CarriesTheWholeRow pins the wire shape against the workflows
// row, which IS the run record. A field silently dropped here is invisible to
// an API consumer and looks like the engine losing information.
func TestRunToProto_CarriesTheWholeRow(t *testing.T) {
	t.Parallel()

	created := time.Now().Add(-time.Hour)
	completed := time.Now()

	run := &core.Workflow{
		ID:              "run-1",
		ParentID:        strptr("run-parent"),
		ChatID:          "session-1",
		WorkflowName:    "builtin://agent",
		Thread:          "thread-1",
		Status:          core.Completed(),
		SpawnedByNodeID: strptr("node-7"),
		LoopIteration:   i64ptr(3),
		CreatedAt:       created,
		CompletedAt:     &completed,
		Outcome:         strptr("success"),
	}

	got := runToProto(run)
	require.NotNil(t, got)

	assert.Equal(t, "run-1", got.Id)
	assert.Equal(t, "builtin://agent", got.WorkflowName)
	assert.Equal(t, "thread-1", got.Thread)
	assert.Equal(t, "session-1", got.SessionId)
	assert.Equal(t, "run-parent", got.ParentId)
	assert.Equal(t, "node-7", got.SpawnedByNodeId)
	assert.Equal(t, int64(3), got.LoopIteration)
	assert.Equal(t, "success", got.Outcome)
	assert.Equal(t, created.UnixMilli(), got.CreatedAtMs)
	assert.Equal(t, completed.UnixMilli(), got.CompletedAtMs)
}

// TestRunToProto_NilOptionalsAreEmpty covers a root run: no parent, no spawning
// node, no loop, no outcome, not finished. Dereferencing any of those would
// panic on the most common run there is.
func TestRunToProto_NilOptionalsAreEmpty(t *testing.T) {
	t.Parallel()

	got := runToProto(&core.Workflow{
		ID:           "run-root",
		ChatID:       "session-1",
		WorkflowName: "builtin://agent",
		Thread:       "thread-1",
		Status:       core.Active(),
		CreatedAt:    time.Now(),
	})
	require.NotNil(t, got)

	assert.Empty(t, got.ParentId)
	assert.Empty(t, got.SpawnedByNodeId)
	assert.Zero(t, got.LoopIteration)
	assert.Empty(t, got.Outcome)
	assert.Zero(t, got.CompletedAtMs)
}

func TestRunToProto_NilRun(t *testing.T) {
	t.Parallel()
	assert.Nil(t, runToProto(nil))
}

// TestRunStatusMapping_CoversEveryState is the load-bearing one: an unmapped
// state silently becomes UNSPECIFIED, which an API consumer reads as "unknown"
// for a run that is actually executing.
//
// The pause case matters most. A paused run is STOPPED but still LIVE, and the
// stop reason is the only thing carrying that — collapse it and a caller cannot
// tell "parked, will resume" from "finished".
func TestRunStatusMapping_CoversEveryState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		status     core.WorkflowStatus
		wantState  reliantv1.WorkflowState
		wantReason reliantv1.WorkflowStopReason
	}{
		{
			name:       "pending",
			status:     core.Pending(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_PENDING,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED,
		},
		{
			name:       "active",
			status:     core.Active(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED,
		},
		{
			name:       "completed",
			status:     core.Completed(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED,
		},
		{
			name:       "failed",
			status:     core.Failed(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED,
		},
		{
			name:       "paused is stopped but live",
			status:     core.Paused(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED,
		},
		{
			name:       "cancelled",
			status:     core.Cancelled(),
			wantState:  reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED,
			wantReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runToProto(&core.Workflow{Status: tc.status, CreatedAt: time.Now()})
			assert.Equal(t, tc.wantState, got.State)
			assert.Equal(t, tc.wantReason, got.StopReason)
		})
	}
}

// TestFilterRuns_StateFilter pins that the filter selects rather than truncates.
func TestFilterRuns_StateFilter(t *testing.T) {
	t.Parallel()

	runs := []*core.Workflow{
		{ID: "a", Status: core.Active(), CreatedAt: time.Now()},
		{ID: "b", Status: core.Completed(), CreatedAt: time.Now()},
		{ID: "c", Status: core.Active(), CreatedAt: time.Now()},
	}

	got := filterRunsToProto(runs, &reliantv1.ListRunsRequest{
		State: reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE,
	})

	require.Len(t, got.Runs, 2)
	assert.Equal(t, int32(2), got.Total)
	assert.Equal(t, "a", got.Runs[0].Id)
	assert.Equal(t, "c", got.Runs[1].Id)
}

// TestFilterRuns_UnspecifiedStateReturnsAll — the filter is opt-in, so an
// unset state must not silently return nothing.
func TestFilterRuns_UnspecifiedStateReturnsAll(t *testing.T) {
	t.Parallel()

	runs := []*core.Workflow{
		{ID: "a", Status: core.Active(), CreatedAt: time.Now()},
		{ID: "b", Status: core.Completed(), CreatedAt: time.Now()},
	}

	got := filterRunsToProto(runs, &reliantv1.ListRunsRequest{})
	assert.Len(t, got.Runs, 2)
	assert.Equal(t, int32(2), got.Total)
}

// TestFilterRuns_TotalIsPreFilterOfTheWindow pins that Total counts everything
// matching the filter, not just the page — a pager that reads Total as the page
// size stops early.
func TestFilterRuns_TotalIsPreFilterOfTheWindow(t *testing.T) {
	t.Parallel()

	runs := make([]*core.Workflow, 0, 5)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		runs = append(runs, &core.Workflow{ID: id, Status: core.Active(), CreatedAt: time.Now()})
	}

	got := filterRunsToProto(runs, &reliantv1.ListRunsRequest{Limit: 2})
	assert.Len(t, got.Runs, 2, "the page is limited")
	assert.Equal(t, int32(5), got.Total, "the total counts every match")
}

// TestFilterRuns_OffsetBeyondEndIsEmptyNotPanic — paging past the end is a
// normal request from a client that raced a deletion.
func TestFilterRuns_OffsetBeyondEndIsEmptyNotPanic(t *testing.T) {
	t.Parallel()

	runs := []*core.Workflow{{ID: "a", Status: core.Active(), CreatedAt: time.Now()}}

	got := filterRunsToProto(runs, &reliantv1.ListRunsRequest{Offset: 50})
	assert.Empty(t, got.Runs)
	assert.Equal(t, int32(1), got.Total)
}

// TestFilterRuns_SkipsNilRows guards the conversion against a nil element,
// which a partial DB read can produce.
func TestFilterRuns_SkipsNilRows(t *testing.T) {
	t.Parallel()

	runs := []*core.Workflow{
		{ID: "a", Status: core.Active(), CreatedAt: time.Now()},
		nil,
		{ID: "c", Status: core.Active(), CreatedAt: time.Now()},
	}

	got := filterRunsToProto(runs, &reliantv1.ListRunsRequest{})
	require.Len(t, got.Runs, 2)
	assert.Equal(t, "a", got.Runs[0].Id)
	assert.Equal(t, "c", got.Runs[1].Id)
}
