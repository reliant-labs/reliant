// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
)

func conditionNode(id, expr string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:        id,
		Type:      model.NodeTypeWorkflow,
		Condition: &reliantv1.DirectCelBool{Expr: expr},
	}
}

// TestNodeConditionSeesIterAndPreviousIterationOutputs closes a gap that failed
// SILENTLY, which is the only reason it survived.
//
// Inside a loop, EvaluateNodeConfig already resolves a node's config templates
// against `iter` and `outputs` (the previous iteration's declared loop outputs) —
// that is how get-it-right's retry prompt reports which lane failed. A node's
// `condition` went through a different path that populated neither. It did not
// error: EdgeEvalContext.Namespaces() declares `iter` and `outputs`
// unconditionally, so `outputs.eval_strategy` COMPILED and then evaluated against
// an empty map. Every has() guard false, every branch taken as though this were
// iteration zero, forever.
//
// So a node's condition could see strictly less than the same node's own inject —
// and a condition is the one place a workflow can say "skip this node THIS time
// round", which makes it exactly the place that has to know which time round it is.
// get-it-right's re-review entry is that expression: `implement` is skipped only on
// the iteration after the REVIEWER declared itself stuck and a human answered.
func TestNodeConditionSeesIterAndPreviousIterationOutputs(t *testing.T) {
	t.Parallel()
	inputs := map[string]interface{}{"max_retries": 5}

	t.Run("outputs carries the previous iteration", func(t *testing.T) {
		// The literal expression get-it-right ships on `implement`.
		const rereviewGuard = "!(has(outputs.eval_strategy) && outputs.eval_strategy == 'stuck' && " +
			"has(outputs.has_feedback) && outputs.has_feedback)"
		node := conditionNode("implement", rereviewGuard)

		for _, tc := range []struct {
			name    string
			scope   *LoopScope
			wantRun bool
		}{
			{
				name:    "iteration 0: nothing has happened yet",
				scope:   &LoopScope{Iter: &model.IterContext{Iteration: 0}, Outputs: map[string]interface{}{}},
				wantRun: true,
			},
			{
				name: "the reviewer asked for changes — normal retry, implement runs",
				scope: &LoopScope{Iter: &model.IterContext{Iteration: 1}, Outputs: map[string]interface{}{
					"eval_strategy": "continue", "has_feedback": false,
				}},
				wantRun: true,
			},
			{
				name: "stuck, but nobody has answered yet",
				scope: &LoopScope{Iter: &model.IterContext{Iteration: 1}, Outputs: map[string]interface{}{
					"eval_strategy": "stuck", "has_feedback": false,
				}},
				wantRun: true,
			},
			{
				name: "stuck and the human answered — THIS is the re-review iteration",
				scope: &LoopScope{Iter: &model.IterContext{Iteration: 1}, Outputs: map[string]interface{}{
					"eval_strategy": "stuck", "has_feedback": true,
				}},
				wantRun: false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := evaluateNodeCondition(node, nil, inputs, nil, tc.scope)
				require.NoError(t, err)
				require.Equal(t, tc.wantRun, got)
			})
		}
	})

	t.Run("iter carries the iteration number", func(t *testing.T) {
		node := conditionNode("first_only", "iter.iteration == 0")

		got, err := evaluateNodeCondition(node, nil, inputs, nil,
			&LoopScope{Iter: &model.IterContext{Iteration: 0}})
		require.NoError(t, err)
		require.True(t, got)

		got, err = evaluateNodeCondition(node, nil, inputs, nil,
			&LoopScope{Iter: &model.IterContext{Iteration: 3, Index: 3}})
		require.NoError(t, err)
		require.False(t, got, "iter must be the CURRENT iteration, not a zero value")
	})

	// The silent half used to be that `outputs` was declared everywhere, so a
	// scopeless condition compiled and quietly read an empty map. `outputs` is
	// now declared exactly where it is populated: outside a loop body a
	// reference is a compile error (which validation reports up front), and
	// inside one it is never undeclared — loopBodyScope makes iteration 0 the
	// empty map.
	t.Run("outside a loop body outputs is undeclared", func(t *testing.T) {
		node := conditionNode("guarded", "has(outputs.eval_strategy)")
		_, err := evaluateNodeCondition(node, nil, inputs, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "undeclared reference to 'outputs'")
	})

	t.Run("a loop body at iteration 0 declares outputs as the empty map", func(t *testing.T) {
		node := conditionNode("guarded", "!has(outputs.eval_strategy)")
		got, err := evaluateNodeCondition(node, nil, inputs, nil,
			loopBodyScope(&model.IterContext{Iteration: 0}, nil))
		require.NoError(t, err)
		require.True(t, got)
	})
}
