// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// WHILE CONDITION EVALUATION (via typed workflow CEL API)
// =============================================================================

func TestWhileConditionViaV2celAPI(t *testing.T) {
	t.Parallel()
	// -------------------------------------------------------------------------
	// The exact agent.yaml while condition
	// -------------------------------------------------------------------------
	t.Run("agent while: tool_calls present and under max_turns", func(t *testing.T) {
		outputs := map[string]interface{}{
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":    "toolu_123",
					"name":  "Bash",
					"type":  "tool_use",
					"input": map[string]interface{}{"command": "echo test"},
				},
			},
		}
		inputs := map[string]interface{}{"max_turns": int64(200)}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 1}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "should continue: tool_calls exist and iteration 1 < 200")
	})

	t.Run("agent while: no tool_calls", func(t *testing.T) {
		outputs := map[string]interface{}{
			"tool_calls": []interface{}{},
		}
		inputs := map[string]interface{}{"max_turns": int64(200)}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 1}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "should stop: no tool_calls")
	})

	t.Run("agent while: null tool_calls", func(t *testing.T) {
		outputs := map[string]interface{}{
			"tool_calls": nil,
		}
		inputs := map[string]interface{}{"max_turns": int64(200)}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 1}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "should stop: tool_calls is null")
	})

	t.Run("agent while: at max_turns boundary", func(t *testing.T) {
		outputs := map[string]interface{}{
			"tool_calls": []interface{}{map[string]interface{}{"id": "t1"}},
		}
		inputs := map[string]interface{}{"max_turns": int64(5)}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 4}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "iteration 4 < max_turns 5: should continue")

		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 5}, Outputs: outputs, Inputs: inputs}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "iteration 5 < max_turns 5: should stop")

		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 6}, Outputs: outputs, Inputs: inputs}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "iteration 6 < max_turns 5: should stop")
	})

	// -------------------------------------------------------------------------
	// Simple conditions
	// -------------------------------------------------------------------------
	t.Run("simple exit_code condition", func(t *testing.T) {
		outputs := map[string]interface{}{"exit_code": int64(1)}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}

		result, err := wfcel.EvaluateBool("outputs.exit_code != 0", ctx)
		require.NoError(t, err)
		assert.True(t, result, "exit_code 1 != 0: should continue")

		outputs["exit_code"] = int64(0)
		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}
		result, err = wfcel.EvaluateBool("outputs.exit_code != 0", ctx)
		require.NoError(t, err)
		assert.False(t, result, "exit_code 0 != 0: should stop")
	})

	t.Run("iteration only condition", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 2}, Outputs: map[string]interface{}{}, Inputs: map[string]interface{}{}}
		result, err := wfcel.EvaluateBool("iter.iteration < 3", ctx)
		require.NoError(t, err)
		assert.True(t, result, "2 < 3: should continue")

		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 3}, Outputs: map[string]interface{}{}, Inputs: map[string]interface{}{}}
		result, err = wfcel.EvaluateBool("iter.iteration < 3", ctx)
		require.NoError(t, err)
		assert.False(t, result, "3 < 3: should stop")
	})

	// -------------------------------------------------------------------------
	// Type coercion edge cases
	// -------------------------------------------------------------------------
	t.Run("max_turns as float64 (JSON deserialization)", func(t *testing.T) {
		outputs := map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"id": "t1"}}}
		inputs := map[string]interface{}{"max_turns": float64(5)}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 3}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "3 < 5.0 with cross-type comparisons: should continue")
	})

	t.Run("max_turns as int (not int64)", func(t *testing.T) {
		outputs := map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"id": "t1"}}}
		inputs := map[string]interface{}{"max_turns": 5}
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 3}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "3 < 5 with plain int: should work")
	})

	t.Run("exit_code as float64", func(t *testing.T) {
		outputs := map[string]interface{}{"exit_code": float64(1)}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}

		result, err := wfcel.EvaluateBool("outputs.exit_code != 0", ctx)
		require.NoError(t, err)
		assert.True(t, result, "float64(1) != 0: should work with cross-type comparisons")
	})

	// -------------------------------------------------------------------------
	// Nested field access
	// -------------------------------------------------------------------------
	t.Run("nested output field: outputs.message.role", func(t *testing.T) {
		outputs := map[string]interface{}{
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "Hello",
			},
		}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}
		result, err := wfcel.EvaluateBool("outputs.message.role == 'assistant'", ctx)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("nested input field: inputs.model.id", func(t *testing.T) {
		inputs := map[string]interface{}{
			"model": map[string]interface{}{
				"id": "claude-3",
			},
		}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: map[string]interface{}{}, Inputs: inputs}
		result, err := wfcel.EvaluateBool("inputs.model.id == 'claude-3'", ctx)
		require.NoError(t, err)
		assert.True(t, result)
	})

	// -------------------------------------------------------------------------
	// Boolean logic (compound conditions)
	// -------------------------------------------------------------------------
	t.Run("compound AND condition", func(t *testing.T) {
		outputs := map[string]interface{}{"exit_code": int64(0), "succeeded": true}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}

		result, err := wfcel.EvaluateBool("outputs.exit_code == 0 && outputs.succeeded == true", ctx)
		require.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("compound OR condition", func(t *testing.T) {
		outputs := map[string]interface{}{"exit_code": int64(1), "retry": true}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}

		result, err := wfcel.EvaluateBool("outputs.exit_code == 0 || outputs.retry == true", ctx)
		require.NoError(t, err)
		assert.True(t, result, "retry is true even though exit_code != 0")
	})

	t.Run("ternary-like condition", func(t *testing.T) {
		outputs := map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"id": "t1"}}}
		inputs := map[string]interface{}{"max_turns": int64(10)}

		expr := "size(outputs.tool_calls) > 0 ? iter.iteration < inputs.max_turns : false"
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 3}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "tool_calls exist and 3 < 10")
	})

	// -------------------------------------------------------------------------
	// Edge conditions
	// -------------------------------------------------------------------------
	t.Run("empty outputs map", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: map[string]interface{}{}, Inputs: map[string]interface{}{}}
		_, err := wfcel.EvaluateBool("outputs.tool_calls != null", ctx)
		assert.Error(t, err, "accessing missing key should error")
	})

	t.Run("iteration 0 boundary", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: map[string]interface{}{}, Inputs: map[string]interface{}{}}
		result, err := wfcel.EvaluateBool("iter.iteration < 1", ctx)
		require.NoError(t, err)
		assert.True(t, result, "0 < 1")
	})

	t.Run("boolean output value", func(t *testing.T) {
		outputs := map[string]interface{}{"should_continue": true}
		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}
		result, err := wfcel.EvaluateBool("outputs.should_continue", ctx)
		require.NoError(t, err)
		assert.True(t, result)

		outputs["should_continue"] = false
		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: map[string]interface{}{}}
		result, err = wfcel.EvaluateBool("outputs.should_continue", ctx)
		require.NoError(t, err)
		assert.False(t, result)
	})
}

// =============================================================================
// CEL TYPE SAFETY — regression tests
// =============================================================================

func TestCELTypeSafety(t *testing.T) {
	t.Parallel()
	t.Run("workflow CEL EvaluateBool with typed IterContext works", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{"exit_code": int64(1)},
			Iter:    &model.IterContext{Iteration: 2},
			Inputs:  map[string]interface{}{"max_turns": int64(10)},
		}

		result, err := wfcel.EvaluateBool("iter.iteration < inputs.max_turns", ctx)
		require.NoError(t, err)
		assert.True(t, result, "2 < 10")
	})

	t.Run("workflow CEL EvaluateBool with iteration boundaries", func(t *testing.T) {
		expr := "iter.iteration < 3"

		cases := []struct {
			iteration int
			expected  bool
		}{
			{0, true},
			{1, true},
			{2, true},
			{3, false},
			{4, false},
			{100, false},
		}
		for _, tc := range cases {
			ctx := &wfcel.LoopEvalContext{
				Iter:    &model.IterContext{Iteration: tc.iteration},
				Outputs: map[string]interface{}{},
				Inputs:  map[string]interface{}{},
			}
			result, err := wfcel.EvaluateBool(expr, ctx)
			require.NoError(t, err, "iteration %d", tc.iteration)
			assert.Equal(t, tc.expected, result, "iteration %d: expected %v", tc.iteration, tc.expected)
		}
	})
}

// =============================================================================
// COMPLEX REAL-WORLD PATTERNS
// =============================================================================

func TestCELRealWorldPatterns(t *testing.T) {
	t.Parallel()
	t.Run("TDD loop: exit when tests fail", func(t *testing.T) {
		expr := "outputs.exit_code != 0"

		// Tests pass (exit_code=0) → continue RED phase (keep trying to make them fail)
		ctx := &wfcel.LoopEvalContext{
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{"exit_code": int64(0)},
			Inputs:  map[string]interface{}{},
		}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "exit_code=0: tests pass, stop")

		// Tests fail (exit_code=1) → RED phase succeeded
		ctx = &wfcel.LoopEvalContext{
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{"exit_code": int64(1)},
			Inputs:  map[string]interface{}{},
		}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "exit_code=1: tests fail, continue")
	})

	t.Run("debate loop: iteration < max_iterations - 1", func(t *testing.T) {
		expr := "iter.iteration < inputs.max_iterations - 1"
		inputs := map[string]interface{}{"max_iterations": int64(3)}
		outputs := map[string]interface{}{}

		ctx := &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 0}, Outputs: outputs, Inputs: inputs}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "0 < 3-1=2: continue")

		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 1}, Outputs: outputs, Inputs: inputs}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "1 < 2: continue")

		ctx = &wfcel.LoopEvalContext{Iter: &model.IterContext{Iteration: 2}, Outputs: outputs, Inputs: inputs}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "2 < 2: stop")
	})

	t.Run("agent loop with multiple output checks", func(t *testing.T) {
		expr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

		ctx := &wfcel.LoopEvalContext{
			Iter: &model.IterContext{Iteration: 5},
			Outputs: map[string]interface{}{
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "t1", "name": "Bash", "type": "tool_use"},
					map[string]interface{}{"id": "t2", "name": "Edit", "type": "tool_use"},
				},
				"response_text": "I'll run that command.",
			},
			Inputs: map[string]interface{}{"max_turns": int64(200)},
		}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "2 tool calls and iteration 5 < 200: continue")

		ctx = &wfcel.LoopEvalContext{
			Iter: &model.IterContext{Iteration: 5},
			Outputs: map[string]interface{}{
				"tool_calls":    []interface{}{},
				"response_text": "Done! Here's what I did.",
			},
			Inputs: map[string]interface{}{"max_turns": int64(200)},
		}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "no tool calls: stop")
	})

	t.Run("has() function for optional fields", func(t *testing.T) {
		expr := "has(outputs.exit_code) ? outputs.exit_code != 0 : true"

		// When exit_code is missing, continue (default true)
		ctx := &wfcel.LoopEvalContext{
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{},
			Inputs:  map[string]interface{}{},
		}
		result, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.True(t, result, "missing exit_code: default true")

		// When exit_code exists and is 0, stop
		ctx = &wfcel.LoopEvalContext{
			Iter:    &model.IterContext{Iteration: 0},
			Outputs: map[string]interface{}{"exit_code": int64(0)},
			Inputs:  map[string]interface{}{},
		}
		result, err = wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err)
		assert.False(t, result, "exit_code=0: stop")
	})
}
