// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// TestLoopIteration0_OutputsNamespaceDeclared verifies that the outputs namespace
// is declared (as an empty map) on loop iteration 0, so CEL expressions that
// reference outputs.* behind a ternary guard can compile successfully.
//
// Background: CEL has two phases — compilation and evaluation. Compilation
// requires all referenced variables to be declared, even in branches that won't
// execute. A ternary like `iter.iteration == 0 ? "first" : outputs.feedback`
// fails compilation if outputs is not declared, even though the false branch
// is never evaluated on iteration 0.
func TestLoopIteration0_OutputsNamespaceDeclared(t *testing.T) {
	t.Run("ternary guarding outputs compiles on iteration 0", func(t *testing.T) {
		node := makeProtoTestNode("write_deck", "builtin://agent",
			&reliantv1.InjectConfig{
				Role: makeCelLiteral("user"),
				Content: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{
						Expr: `{{iter.iteration == 0 ? "Write the first draft." : "Revise based on: " + outputs.review_feedback}}`,
					},
				},
			}, false)

		evalResult, err := EvaluateNodeConfig(
			node,
			map[string]interface{}{}, // nodeOutputs
			"test-workflow-id",       // workflowID
			"test-workflow",          // workflowName
			map[string]interface{}{}, // inputs
			map[string]interface{}{ // iterContext — loop iteration 0
				"iteration": 0,
			},
			nil, // loopOutputs — nil on iteration 0
			nil, // execContext
		)

		require.NoError(t, err, "CEL compilation should succeed — outputs must be declared even on iteration 0")
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)
		assert.Equal(t, "Write the first draft.", model.CelStringValue(inject.GetContent()))
	})

	t.Run("ternary accesses outputs on iteration 1", func(t *testing.T) {
		node := makeProtoTestNode("write_deck", "builtin://agent",
			&reliantv1.InjectConfig{
				Role: makeCelLiteral("user"),
				Content: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{
						Expr: `{{iter.iteration == 0 ? "Write the first draft." : "Revise based on: " + outputs.review_feedback}}`,
					},
				},
			}, false)

		evalResult, err := EvaluateNodeConfig(
			node,
			map[string]interface{}{}, // nodeOutputs
			"test-workflow-id",       // workflowID
			"test-workflow",          // workflowName
			map[string]interface{}{}, // inputs
			map[string]interface{}{ // iterContext — loop iteration 1
				"iteration": 1,
			},
			map[string]interface{}{ // loopOutputs — has data from iteration 0
				"review_feedback": "Needs more market data",
			},
			nil, // execContext
		)

		require.NoError(t, err)
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)
		assert.Equal(t, "Revise based on: Needs more market data", model.CelStringValue(inject.GetContent()))
	})

	t.Run("outputs not declared outside loop context", func(t *testing.T) {
		node := makeProtoTestNode("some_node", "builtin://agent",
			&reliantv1.InjectConfig{
				Role: makeCelLiteral("user"),
				Content: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{
						Expr: `{{outputs.some_field}}`,
					},
				},
			}, false)

		_, err := EvaluateNodeConfig(
			node,
			map[string]interface{}{}, // nodeOutputs
			"test-workflow-id",       // workflowID
			"test-workflow",          // workflowName
			map[string]interface{}{}, // inputs
			nil,                      // iterContext — NOT in a loop
			nil,                      // loopOutputs
			nil,                      // execContext
		)

		require.Error(t, err, "outputs should not be declared outside of loop context")
		assert.Contains(t, err.Error(), "outputs")
	})
}
