// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEvaluateNodeCondition tests the node condition evaluation logic
func TestEvaluateNodeCondition(t *testing.T) {
	t.Run("empty condition returns true", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run"}
		result, err := evaluateNodeCondition(node, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("literal true returns true", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "true"}}
		result, err := evaluateNodeCondition(node, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("literal false returns false", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "false"}}
		result, err := evaluateNodeCondition(node, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("CEL expression with inputs - membership check", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "'research' in inputs.phases"}}
		workflowInputs := map[string]interface{}{
			"phases": []interface{}{"research", "plan", "implement"},
		}

		result, err := evaluateNodeCondition(node, nil, workflowInputs, nil, nil)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("CEL expression with inputs - membership check false", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "'audit' in inputs.phases"}}
		workflowInputs := map[string]interface{}{
			"phases": []interface{}{"research", "plan", "implement"},
		}

		result, err := evaluateNodeCondition(node, nil, workflowInputs, nil, nil)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("CEL expression with nodes - exit code check", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.build.exit_code == 0"}}
		nodeOutputs := map[string]interface{}{
			"build": map[string]interface{}{
				"exit_code": 0,
			},
		}

		result, err := evaluateNodeCondition(node, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("CEL expression with nodes - exit code non-zero", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.build.exit_code == 0"}}
		nodeOutputs := map[string]interface{}{
			"build": map[string]interface{}{
				"exit_code": 1,
			},
		}

		result, err := evaluateNodeCondition(node, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("CEL expression checking skipped output", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "!has(nodes.previous) || !nodes.previous.skipped"}}
		nodeOutputs := map[string]interface{}{
			"previous": map[string]interface{}{
				"skipped": true,
			},
		}

		result, err := evaluateNodeCondition(node, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result) // Should not execute because previous was skipped
	})

	t.Run("CEL expression with boolean input", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "inputs.run_tests"}}
		workflowInputs := map[string]interface{}{
			"run_tests": true,
		}

		result, err := evaluateNodeCondition(node, nil, workflowInputs, nil, nil)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("CEL expression with boolean input false", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "inputs.run_tests"}}
		workflowInputs := map[string]interface{}{
			"run_tests": false,
		}

		result, err := evaluateNodeCondition(node, nil, workflowInputs, nil, nil)
		require.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("invalid CEL expression returns error", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "this is not valid CEL"}}

		_, err := evaluateNodeCondition(node, nil, nil, nil, nil)
		require.Error(t, err)
	})

	t.Run("CEL expression that doesn't return boolean returns error", func(t *testing.T) {
		node := &reliantv1.Node{Id: "test", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "'not a boolean'"}}

		_, err := evaluateNodeCondition(node, nil, nil, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not return boolean")
	})

	t.Run("complex multi-select scenario", func(t *testing.T) {
		phases := []interface{}{"research", "plan", "implement", "test", "lint", "build"}
		workflowInputs := map[string]interface{}{
			"phases": phases,
		}

		testCases := []struct {
			nodeID    string
			condition string
			expected  bool
		}{
			{"research", "'research' in inputs.phases", true},
			{"plan", "'plan' in inputs.phases", true},
			{"criticize", "'criticize' in inputs.phases", false},
			{"revise", "'revise' in inputs.phases", false},
			{"implement", "'implement' in inputs.phases", true},
			{"test", "'test' in inputs.phases", true},
			{"lint", "'lint' in inputs.phases", true},
			{"build", "'build' in inputs.phases", true},
			{"audit", "'audit' in inputs.phases", false},
		}

		for _, tc := range testCases {
			t.Run(tc.nodeID, func(t *testing.T) {
				node := &reliantv1.Node{Id: tc.nodeID, Type: "run", Condition: &reliantv1.DirectCelBool{Expr: tc.condition}}
				result, err := evaluateNodeCondition(node, nil, workflowInputs, nil, nil)
				require.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			})
		}
	})
}

// TestSkippedRunNodeOutputs tests that skipped run nodes have type-aware outputs
func TestSkippedRunNodeOutputs(t *testing.T) {
	t.Run("skipped run node has exit_code accessible in downstream CEL", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"lint": model.SkippedRunOutputMap(),
		}

		// Downstream node checks lint.exit_code — should work without has() guards
		downstream := &reliantv1.Node{Id: "deploy", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.exit_code == 0"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "skipped run node exit_code should be 0")
	})

	t.Run("skipped run node is still detected as skipped", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"lint": model.SkippedRunOutputMap(),
		}

		downstream := &reliantv1.Node{Id: "deploy", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.skipped"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "skipped field should still be true")
	})

	t.Run("skipped non-run node does not have exit_code", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"llm_step": model.SkippedOutputMap(),
		}

		// Accessing exit_code on a skipped non-run node should fail
		downstream := &reliantv1.Node{Id: "next", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "has(nodes.llm_step.exit_code)"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result, "non-run skipped node should not have exit_code")
	})

	t.Run("skipped run node stdout is empty string", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"build": model.SkippedRunOutputMap(),
		}

		downstream := &reliantv1.Node{Id: "check", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.build.stdout == ''"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "skipped run node stdout should be empty")
	})
}

func TestSkippedNodeNegativeEdgeCases(t *testing.T) {
	t.Run("skipped non-run node does NOT have exit_code field", func(t *testing.T) {
		// SkippedOutputMap() only has {"skipped": true}.
		// Accessing exit_code should fail or return false with has().
		nodeOutputs := map[string]interface{}{
			"msg_node": model.SkippedOutputMap(),
		}

		// has(nodes.msg_node.exit_code) should be false
		downstream := &reliantv1.Node{Id: "next", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "has(nodes.msg_node.exit_code)"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result, "skipped non-run node should NOT have exit_code")
	})

	t.Run("skipped run node has both exit_code 0 and skipped true", func(t *testing.T) {
		// SkippedRunOutputMap() has {"skipped": true, "exit_code": 0, ...}.
		// Both fields should be accessible in CEL.
		nodeOutputs := map[string]interface{}{
			"lint": model.SkippedRunOutputMap(),
		}

		// Check exit_code == 0
		downstream := &reliantv1.Node{Id: "check1", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.exit_code == 0"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "skipped run node exit_code should be 0")

		// Check skipped == true
		downstream2 := &reliantv1.Node{Id: "check2", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.skipped"}}
		result2, err := evaluateNodeCondition(downstream2, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result2, "skipped run node should have skipped == true")

		// Check both coexist: exit_code == 0 && skipped == true
		downstream3 := &reliantv1.Node{Id: "check3", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.exit_code == 0 && nodes.lint.skipped"}}
		result3, err := evaluateNodeCondition(downstream3, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result3, "both exit_code == 0 and skipped should coexist")
	})

	t.Run("non-skipped run node does NOT have skipped == true", func(t *testing.T) {
		// A run node that actually executed should NOT have skipped == true.
		nodeOutputs := map[string]interface{}{
			"build": map[string]interface{}{
				"exit_code": 0,
				"stdout":    "build succeeded",
				"stderr":    "",
			},
		}

		// has(nodes.build.skipped) should be false for a real (non-skipped) node
		downstream := &reliantv1.Node{Id: "next", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "!has(nodes.build.skipped) || !nodes.build.skipped"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "non-skipped run node should not have skipped == true")
	})

	t.Run("skipped run node exit_code != 0 is false confirming skipped = did not fail", func(t *testing.T) {
		// Skipped run nodes have exit_code = 0.
		// The expression `nodes.lint.exit_code != 0` should be false,
		// confirming the "skipped = didn't fail" semantics.
		nodeOutputs := map[string]interface{}{
			"lint": model.SkippedRunOutputMap(),
		}

		downstream := &reliantv1.Node{Id: "fix", Type: "run", Condition: &reliantv1.DirectCelBool{Expr: "nodes.lint.exit_code != 0"}}
		result, err := evaluateNodeCondition(downstream, nodeOutputs, nil, nil, nil)
		require.NoError(t, err)
		assert.False(t, result, "skipped run node exit_code != 0 should be false (0 != 0 is false)")
	})
}

// TestNodeConditionInWorkflow tests that conditions are correctly parsed from workflow YAML
func TestNodeConditionInWorkflow(t *testing.T) {
	t.Run("parse workflow with condition", func(t *testing.T) {
		yamlData := []byte(`
name: test-workflow
apiVersion: "0.0.5"
entry: [step1]
nodes:
  - id: step1
    condition: "inputs.run_step1"
    type: test_action
  - id: step2
    type: test_action_2
edges: []
`)

		wf, err := ParseWorkflowProtoBytesNoValidation(yamlData)
		require.NoError(t, err)
		require.Len(t, wf.Nodes, 2)

		assert.Equal(t, "inputs.run_step1", model.ConditionExpr(wf.Nodes[0]))
		assert.Equal(t, "", model.ConditionExpr(wf.Nodes[1]))
	})

	t.Run("parse loop node with condition to skip", func(t *testing.T) {
		yamlData := []byte(`
name: test-loop-condition
apiVersion: "0.0.5"
entry: [retry_loop]
nodes:
  - id: retry_loop
    type: loop
    condition: "inputs.enable_retries == true"
    while: "outputs.exit_code != 0 && iter.iteration + 1 < 5"
    inline:
      entry: [attempt]
      outputs:
        exit_code: "{{nodes.run_test.exit_code}}"
      nodes:
        - id: attempt
          type: run
          command: "echo test"
        - id: run_test
          type: run
          command: "exit 0"
      edges:
        - from: attempt
          cases:
            - to: run_test
edges: []
`)

		wf, err := ParseWorkflowProtoBytesNoValidation(yamlData)
		require.NoError(t, err)
		require.Len(t, wf.Nodes, 1)

		loopNode := wf.Nodes[0]
		assert.Equal(t, "retry_loop", loopNode.GetId())
		assert.Equal(t, model.NodeTypeLoop, loopNode.GetType())
		assert.Equal(t, "inputs.enable_retries == true", model.ConditionExpr(loopNode))
		assert.Equal(t, "outputs.exit_code != 0 && iter.iteration + 1 < 5", loopNode.GetLoop().GetWhile().GetExpr())

		workflowInputsFalse := map[string]interface{}{"enable_retries": false}
		result, err := evaluateNodeCondition(loopNode, nil, workflowInputsFalse, nil, nil)
		require.NoError(t, err)
		assert.False(t, result, "Loop should be skipped when enable_retries is false")

		workflowInputsTrue := map[string]interface{}{"enable_retries": true}
		result, err = evaluateNodeCondition(loopNode, nil, workflowInputsTrue, nil, nil)
		require.NoError(t, err)
		assert.True(t, result, "Loop should execute when enable_retries is true")
	})
}
