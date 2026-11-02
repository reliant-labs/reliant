// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// is_child is what the product dashboards filter on to count a run once. It
// was hard-coded false (and children were never emitted), so the filter did
// nothing. A run is a child when another workflow or agent started it.

func TestIsChildRun(t *testing.T) {
	cases := []struct {
		name  string
		input WorkflowStatusInput
		want  bool
	}{
		{"root run started by a user", WorkflowStatusInput{WorkflowID: "w"}, false},
		{"sub-agent spawned by a tool call", WorkflowStatusInput{ParentWorkflowID: "p", SpawnedByToolCallID: "tc"}, true},
		{"node of a parent graph", WorkflowStatusInput{ParentWorkflowID: "p", SpawnedByNodeID: "n"}, true},
		{"parent only", WorkflowStatusInput{ParentWorkflowID: "p"}, true},
		{"spawn marker without a parent id still names a spawner", WorkflowStatusInput{SpawnedByToolCallID: "tc"}, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isChildRun(tc.input), tc.name)
	}
}

func TestWorkflowStatus_ProductEventsFlagChildRuns(t *testing.T) {
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	userID := uuid.NewString()
	projectID := uuid.NewString()
	chatID := uuid.NewString()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	rec := newProductEventRecorder()
	activity := NewWorkflowStatusActivity(h.Repo())
	activity.trackerFor = rec.trackerFor

	rootID, childID := uuid.NewString(), uuid.NewString()
	activity.trackAnalytics(ctx, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: rootID, WorkflowName: "builtin://chat", Status: "completed",
	})
	activity.trackAnalytics(ctx, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: childID, WorkflowName: "builtin://sub-agent", Status: "completed",
		ParentWorkflowID: rootID, SpawnedByToolCallID: "tc-1",
	})

	ended := rec.events(t, "product.workflow_ended")
	require.Len(t, ended, 2, "child runs are recorded, not dropped")
	byWorkflow := map[any]map[string]any{}
	for _, evt := range ended {
		byWorkflow[evt["product.workflow_id"]] = evt
	}
	require.Contains(t, byWorkflow, rootID)
	require.Contains(t, byWorkflow, childID)
	assert.Equal(t, false, byWorkflow[rootID]["product.is_child"], "a user-started run is not a child")
	assert.Equal(t, true, byWorkflow[childID]["product.is_child"], "a spawned run is a child")
	assert.Equal(t, true, byWorkflow[childID]["product.success"])

	// The dashboards' filter keeps exactly the root run.
	var counted []any
	for _, evt := range ended {
		if evt["product.is_child"] != true {
			counted = append(counted, evt["product.workflow_id"])
		}
	}
	assert.Equal(t, []any{rootID}, counted)
}
