// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// TestLoopWhileConditionEvaluation tests that the loop's `while` CEL expression
// correctly evaluates against sub-workflow outputs.
//
// This specifically tests CEL evaluation behavior. The `while` condition determines
// whether the loop should continue (true = continue, false = exit).
func TestLoopWhileConditionEvaluation(t *testing.T) {
	t.Run("while: outputs.exit_code != 0 should exit when exit_code is 1", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": 1,
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "Loop should exit when exit_code=1 (1 != 0 is true)")
	})

	t.Run("while: outputs.exit_code != 0 should continue when exit_code is 0", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": 0,
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.False(t, shouldExit, "Loop should NOT exit when exit_code=0 (0 != 0 is false)")
	})

	t.Run("while: outputs.exit_code == 0 should exit when exit_code is 0", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": 0,
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code == 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "Loop should exit when exit_code=0 (0 == 0 is true)")
	})

	t.Run("exit_code as int64 should work", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": int64(1),
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "Loop should exit when exit_code=1 as int64")
	})

	t.Run("exit_code as float64 should work", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": float64(1),
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "Loop should exit when exit_code=1.0 as float64")
	})

	t.Run("missing exit_code should be handled gracefully", func(t *testing.T) {
		outputs := map[string]interface{}{}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		// This might error or return false - either is acceptable
		// The important thing is it doesn't panic
		_, err := wfcel.EvaluateBool(whileExpr, ctx)
		t.Logf("Missing exit_code evaluation result: err=%v", err)
	})

	t.Run("nil exit_code should be handled gracefully", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": nil,
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		_, err := wfcel.EvaluateBool(whileExpr, ctx)
		t.Logf("Nil exit_code evaluation result: err=%v", err)
	})

	t.Run("outputs namespace access should work", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": 1,
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		shouldExit, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "outputs.exit_code access should work")
	})

	t.Run("string exit_code should be converted or handled", func(t *testing.T) {
		outputs := map[string]interface{}{
			"exit_code": "1",
		}

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		whileExpr := "outputs.exit_code != 0"

		// String "1" compared to int 0 - CEL might error or coerce
		result, err := wfcel.EvaluateBool(whileExpr, ctx)
		t.Logf("String exit_code evaluation: result=%v, err=%v", result, err)
	})
}

// TestLoopExitConditionE2E tests the complete flow: output evaluation -> while condition
// This simulates what happens inside InlineLoopExecutor when checking if the loop should exit.
func TestLoopExitConditionE2E(t *testing.T) {
	t.Run("loop exits when while condition becomes false", func(t *testing.T) {
		outputDefs := map[string]string{
			"exit_code": "{{has(nodes.verify_red) ? nodes.verify_red.exit_code : 0}}",
		}

		nodeOutputs := map[string]interface{}{
			"verify_red": map[string]interface{}{
				"exit_code": 1,
				"stdout":    "FAIL: tests failed",
			},
		}

		workflowContext := map[string]interface{}{"workflow": map[string]interface{}{"id": "test"}}

		outputs, err := EvaluateWorkflowOutputs(outputDefs, nodeOutputs, workflowContext)
		require.NoError(t, err)

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		shouldExit, err := wfcel.EvaluateBool("outputs.exit_code != 0", ctx)
		require.NoError(t, err)
		assert.True(t, shouldExit, "Loop should exit when tests fail (exit_code=1)")
		t.Logf("E2E flow works: exit_code=%v, shouldExit=%v", outputs["exit_code"], shouldExit)
	})

	t.Run("loop continues when while condition is true", func(t *testing.T) {
		outputDefs := map[string]string{
			"exit_code": "{{has(nodes.verify_red) ? nodes.verify_red.exit_code : 0}}",
		}

		nodeOutputs := map[string]interface{}{
			"verify_red": map[string]interface{}{
				"exit_code": 0,
				"stdout":    "PASS: all tests passed",
			},
		}

		workflowContext := map[string]interface{}{"workflow": map[string]interface{}{"id": "test"}}

		outputs, err := EvaluateWorkflowOutputs(outputDefs, nodeOutputs, workflowContext)
		require.NoError(t, err)

		ctx := &wfcel.LoopEvalContext{
			Outputs: outputs,
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{},
		}

		shouldExit, err := wfcel.EvaluateBool("outputs.exit_code != 0", ctx)
		require.NoError(t, err)
		assert.False(t, shouldExit, "Loop should continue when tests pass (exit_code=0)")
		t.Logf("E2E flow works: exit_code=%v, shouldExit=%v", outputs["exit_code"], shouldExit)
	})
}

// TestLoopWhileConditionWithInputsMaxIterations tests the specific bug where
// `inputs.max_iterations` in the while condition fails with "no such overload"
// because the value comes through as a string instead of an integer.
func TestLoopWhileConditionWithInputsMaxIterations(t *testing.T) {
	t.Run("while with inputs.max_iterations as integer should work", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Inputs:  map[string]interface{}{"max_iterations": 3},
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
		}

		whileExpr := "iter.iteration < inputs.max_iterations - 1"
		shouldContinue, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err, "CEL should succeed with integer max_iterations")
		assert.True(t, shouldContinue, "0 < 3-1=2 should continue")
	})

	t.Run("while with inputs.max_iterations as int64 should work", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Inputs:  map[string]interface{}{"max_iterations": int64(3)},
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
		}

		whileExpr := "iter.iteration < inputs.max_iterations - 1"
		shouldContinue, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err, "CEL should succeed with int64 max_iterations")
		assert.True(t, shouldContinue, "0 < 3-1=2 should continue")
	})

	t.Run("while with inputs.max_iterations as float64 arithmetic fails", func(t *testing.T) {
		// float64 - int arithmetic fails because CEL doesn't auto-coerce types for arithmetic.
		// Cross-type numeric comparisons only work for comparisons (==, <, >), not arithmetic (-, +).
		// In practice, ApplyDefaults coerces float64 to int64 before evaluation.
		ctx := &wfcel.LoopEvalContext{
			Inputs:  map[string]interface{}{"max_iterations": float64(3)},
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
		}

		whileExpr := "iter.iteration < inputs.max_iterations - 1"
		_, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.Error(t, err, "float64 - int arithmetic should fail (no such overload)")
	})

	t.Run("while with inputs.max_iterations as float64 comparison works", func(t *testing.T) {
		// Pure comparison (no arithmetic) works with cross-type numeric comparisons
		ctx := &wfcel.LoopEvalContext{
			Inputs:  map[string]interface{}{"max_iterations": float64(3)},
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
		}

		whileExpr := "iter.iteration < inputs.max_iterations"
		shouldContinue, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.NoError(t, err, "Pure comparison should work with float64")
		assert.True(t, shouldContinue, "0 < 3.0 should continue")
	})

	t.Run("while with inputs.max_iterations as string should fail", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Inputs:  map[string]interface{}{"max_iterations": "3"},
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
		}

		whileExpr := "iter.iteration < inputs.max_iterations"
		_, err := wfcel.EvaluateBool(whileExpr, ctx)
		require.Error(t, err, "String comparison with int should fail")
	})
}

// TestLoopWhileConditionWithApplyDefaults tests the integration between
// ApplyDefaults type coercion and while condition CEL evaluation.
func TestLoopWhileConditionWithApplyDefaults(t *testing.T) {
	t.Run("ApplyDefaults coerces float64 input to int64 for CEL arithmetic", func(t *testing.T) {
		defaultVal := int64(10)
		schema := map[string]*reliantv1.Input{
			"max_iterations": {
				Type: "integer",
				Config: &reliantv1.Input_IntegerInput{
					IntegerInput: &reliantv1.IntegerInputConfig{Default: &defaultVal},
				},
			},
		}

		inputs := map[string]interface{}{
			"max_iterations": float64(5),
		}

		result := ApplyDefaultsForRuntime(inputs, schema)

		assert.IsType(t, int64(0), result["max_iterations"],
			"float64 should be coerced to int64, got %T", result["max_iterations"])
		assert.Equal(t, int64(5), result["max_iterations"])

		ctx := &wfcel.LoopEvalContext{
			Inputs:  result,
			Iter:    &model.IterContext{Iteration: 2},
			Outputs: map[string]interface{}{},
		}
		shouldExit, err := wfcel.EvaluateBool("iter.iteration >= inputs.max_iterations - 1", ctx)
		require.NoError(t, err, "CEL should succeed after coercion")
		assert.False(t, shouldExit, "2 >= 5-1=4 should be false")
	})

	t.Run("ApplyDefaults coerces default value to int64", func(t *testing.T) {
		defaultVal := int64(3)
		schema := map[string]*reliantv1.Input{
			"max_iterations": {
				Type: "integer",
				Config: &reliantv1.Input_IntegerInput{
					IntegerInput: &reliantv1.IntegerInputConfig{Default: &defaultVal},
				},
			},
		}

		inputs := map[string]interface{}{}

		result := ApplyDefaultsForRuntime(inputs, schema)

		assert.IsType(t, int64(0), result["max_iterations"],
			"default should be coerced to int64, got %T", result["max_iterations"])
		assert.Equal(t, int64(3), result["max_iterations"])

		ctx := &wfcel.LoopEvalContext{
			Inputs:  result,
			Iter:    &model.IterContext{Iteration: 2},
			Outputs: map[string]interface{}{},
		}
		shouldExit, err := wfcel.EvaluateBool("iter.iteration >= inputs.max_iterations - 1", ctx)
		require.NoError(t, err, "CEL should succeed with coerced default")
		assert.True(t, shouldExit, "2 >= 3-1=2 should be true")
	})
}

// TestLoopOutputsFromSubWorkflow tests that sub-workflow outputs are correctly
// collected and passed to the while condition evaluation.
func TestLoopOutputsFromSubWorkflow(t *testing.T) {
	t.Run("EvaluateWorkflowOutputs with template delimiters", func(t *testing.T) {
		outputDefs := map[string]string{
			"exit_code": "{{has(nodes.verify_red) ? nodes.verify_red.exit_code : 0}}",
		}

		nodeOutputs := map[string]interface{}{
			"verify_red": map[string]interface{}{
				"exit_code": 1,
				"stdout":    "FAIL: tests failed",
				"stderr":    "",
			},
		}

		workflowContext := map[string]interface{}{
			"workflow": map[string]interface{}{
				"id": "test-workflow-id",
			},
		}

		outputs, err := EvaluateWorkflowOutputs(outputDefs, nodeOutputs, workflowContext)
		require.NoError(t, err, "Template delimiters should be stripped before CEL evaluation")

		exitCode, ok := outputs["exit_code"]
		require.True(t, ok, "exit_code should be in outputs")
		assert.Equal(t, int64(1), exitCode, "exit_code should be 1 from verify_red step")
	})

	t.Run("EvaluateWorkflowOutputs with simple expression", func(t *testing.T) {
		outputDefs := map[string]string{
			"exit_code": "{{nodes.verify_red.exit_code}}",
		}

		nodeOutputs := map[string]interface{}{
			"verify_red": map[string]interface{}{
				"exit_code": 1,
				"stdout":    "FAIL: tests failed",
				"stderr":    "",
			},
		}

		workflowContext := map[string]interface{}{
			"workflow": map[string]interface{}{
				"id": "test-workflow-id",
			},
		}

		outputs, err := EvaluateWorkflowOutputs(outputDefs, nodeOutputs, workflowContext)
		require.NoError(t, err)

		exitCode, ok := outputs["exit_code"]
		require.True(t, ok, "exit_code should be in outputs")
		t.Logf("Evaluated exit_code: %v (type: %T)", exitCode, exitCode)

		switch v := exitCode.(type) {
		case int:
			assert.Equal(t, 1, v)
		case int64:
			assert.Equal(t, int64(1), v)
		case float64:
			assert.Equal(t, float64(1), v)
		default:
			t.Errorf("Unexpected type for exit_code: %T", exitCode)
		}
	})

	t.Run("EvaluateWorkflowOutputs with conditional expression", func(t *testing.T) {
		outputDefs := map[string]string{
			"exit_code": "{{has(nodes.verify_red) ? nodes.verify_red.exit_code : 0}}",
		}

		nodeOutputs := map[string]interface{}{
			"check_tests_exist": map[string]interface{}{
				"exit_code": 1,
			},
		}

		workflowContext := map[string]interface{}{
			"workflow": map[string]interface{}{
				"id": "test-workflow-id",
			},
		}

		outputs, err := EvaluateWorkflowOutputs(outputDefs, nodeOutputs, workflowContext)
		require.NoError(t, err)

		exitCode, ok := outputs["exit_code"]
		require.True(t, ok, "exit_code should be in outputs")
		t.Logf("Evaluated exit_code (no verify_red): %v (type: %T)", exitCode, exitCode)

		switch v := exitCode.(type) {
		case int:
			assert.Equal(t, 0, v)
		case int64:
			assert.Equal(t, int64(0), v)
		case float64:
			assert.Equal(t, float64(0), v)
		default:
			t.Errorf("Unexpected type for exit_code: %T", exitCode)
		}
	})
}
