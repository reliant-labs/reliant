// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A v3 activity in a loop's FIRST iteration marshals its RuntimeContext with
// loop_iteration omitted (omitempty drops the 0). The loop node id alone says
// "in a loop", so the iteration must read as 0 — not the "not in a loop" -1
// that filed every iteration-0 step row out of its iteration.
func TestExtractActivityInputInfo_FirstIterationOfV3InputIsZero(t *testing.T) {
	t.Parallel()
	info := extractActivityInputInfo(types.ActivityInput{
		Runtime: types.RuntimeContext{
			ChatID:        "chat-1",
			WorkflowID:    "wf-1",
			StepID:        "lint",
			LoopNodeID:    "attempt",
			LoopIteration: 0,
		},
	})

	assert.Equal(t, "attempt", info.LoopNodeID)
	assert.Equal(t, 0, info.LoopIteration, "iteration 0 of a loop is not 'not in a loop'")
}

// The node event of an activity inside a loop says which loop and which
// iteration it ran in, and where in the graph it sits. Without that the
// viewer cannot tell iteration 2's "started" from a replay of iteration 1's,
// and cannot show a loop body running.
func TestNodeExecutionEvent_CarriesLoopScopeAndNodePath(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	repo := &wrapperTestRepo{}
	registry := NewActivityRegistry(repo)
	registerWrapped(env, registry, "ExecuteTools",
		func(context.Context, types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
			return &reliantv1.ExecuteToolsOutput{}, nil
		})

	_, err := env.ExecuteActivity("ExecuteTools", types.ActivityInput{
		Runtime: types.RuntimeContext{
			ChatID:     "chat-1",
			Thread:     "thread-1",
			WorkflowID: "wf-1",
			StepID:     "execute_tools",
			LoopNodeID: "attempt",
			// Iteration 0: omitted on the wire, which is the case that broke.
			LoopIteration: 0,
			NodePath:      "attempt.review.agent_loop.execute_tools",
		},
		Node: &reliantv1.Node{Id: "execute_tools", Type: model.NodeTypeExecuteTools},
	})
	require.NoError(t, err)

	events := repo.nodeEvents()
	require.Len(t, events, 2, "a started and a completed event")
	for _, ev := range events {
		require.NotNil(t, ev.ParentNodeID, "event must name its loop")
		assert.Equal(t, "attempt", *ev.ParentNodeID)
		require.NotNil(t, ev.Iteration, "event must name its iteration")
		assert.Equal(t, 0, *ev.Iteration)
		assert.Equal(t, "attempt.review.agent_loop.execute_tools", ev.Metadata["node_path"])
	}

	rows := repo.stepRows()
	require.Len(t, rows, 1)
	require.True(t, rows[0].LoopIteration.Valid)
	assert.Equal(t, int64(0), rows[0].LoopIteration.Int64, "the step row belongs to iteration 0")
}

// Outside a loop the event carries no loop scope — a root node's status is
// keyed by its node id alone, and must stay that way.
func TestNodeExecutionEvent_RootNodeHasNoLoopScope(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	repo := &wrapperTestRepo{}
	registry := NewActivityRegistry(repo)
	registerWrapped(env, registry, "ExecuteTools",
		func(context.Context, types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
			return &reliantv1.ExecuteToolsOutput{}, nil
		})

	_, err := env.ExecuteActivity("ExecuteTools", types.ActivityInput{
		Runtime: types.RuntimeContext{
			ChatID:     "chat-1",
			Thread:     "thread-1",
			WorkflowID: "wf-1",
			StepID:     "apply_winner",
			NodePath:   "apply_winner",
		},
		Node: &reliantv1.Node{Id: "apply_winner", Type: model.NodeTypeExecuteTools},
	})
	require.NoError(t, err)

	for _, ev := range repo.nodeEvents() {
		assert.Nil(t, ev.ParentNodeID)
		assert.Nil(t, ev.Iteration)
	}
}
