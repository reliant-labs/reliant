// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoization_ReuseThread tests that reuseThread=true (the only mode used by loops)
// causes all iterations to share the SAME thread ID.
// Loops are passthrough — they always inherit the parent's thread.
func TestMemoization_ReuseThread(t *testing.T) {
	t.Parallel()
	t.Run("multiple iterations get same thread ID", func(t *testing.T) {
		parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
			WithLoop("agent_loop", 0)

		// Loops always pass reuseThread=true — all iterations share the same thread
		iter0 := parent.ForIteration(0, true)
		iter1 := parent.ForIteration(1, true)
		iter2 := parent.ForIteration(2, true)
		iter5 := parent.ForIteration(5, true)
		iter99 := parent.ForIteration(99, true)

		// All should have the same thread as parent
		assert.Equal(t, "thread-0", iter0.Thread, "iteration 0 should have parent thread")
		assert.Equal(t, "thread-0", iter1.Thread, "iteration 1 should have parent thread")
		assert.Equal(t, "thread-0", iter2.Thread, "iteration 2 should have parent thread")
		assert.Equal(t, "thread-0", iter5.Thread, "iteration 5 should have parent thread")
		assert.Equal(t, "thread-0", iter99.Thread, "iteration 99 should have parent thread")
	})

	t.Run("reuseThread=true reuses thread across iterations", func(t *testing.T) {
		parent := NewExecutionContext("wf-deterministic", "chat-999", "loop-workflow", "base-thread").
			WithLoop("retry_loop", 0)

		iterations := make([]*ExecutionContext, 10)
		for i := 0; i < 10; i++ {
			iterations[i] = parent.ForIteration(i, true)
		}

		// All iterations should have identical thread IDs
		firstThread := iterations[0].Thread
		for i, iter := range iterations {
			assert.Equal(t, firstThread, iter.Thread,
				"iteration %d should have same thread as iteration 0", i)
		}
	})
}

// TestMemoization_LoopContext tests that LoopContext is properly set during iteration.
func TestMemoization_LoopContext(t *testing.T) {
	t.Parallel()
	t.Run("LoopContext is set with correct iteration number", func(t *testing.T) {
		parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
			WithLoop("test_loop", 0)

		iter7 := parent.ForIteration(7, true)
		iter23 := parent.ForIteration(23, true)

		// Loop context should be set
		require.NotNil(t, iter7.Loop, "iteration 7 should have loop context")
		require.NotNil(t, iter23.Loop, "iteration 23 should have loop context")

		// Iteration numbers should match
		assert.Equal(t, 7, iter7.Loop.Iteration, "iteration 7 should have Iteration=7")
		assert.Equal(t, 23, iter23.Loop.Iteration, "iteration 23 should have Iteration=23")

		// NodeID should be preserved from parent
		assert.Equal(t, "test_loop", iter7.Loop.NodeID, "iteration 7 should preserve parent's NodeID")
	})

	t.Run("IsInLoop returns true for iteration contexts", func(t *testing.T) {
		parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
			WithLoop("agent_loop", 0)

		iter0 := parent.ForIteration(0, true)
		iter5 := parent.ForIteration(5, true)

		assert.True(t, iter0.IsInLoop(), "iteration 0 should report IsInLoop=true")
		assert.True(t, iter5.IsInLoop(), "iteration 5 should report IsInLoop=true")
	})

	t.Run("LoopContext is nil when parent has no loop", func(t *testing.T) {
		// Parent without loop context
		parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")
		assert.Nil(t, parent.Loop, "parent should have no loop context")

		// ForIteration with no parent loop context
		iter := parent.ForIteration(0, true)

		// Loop context should be nil (no loop info to inherit)
		assert.Nil(t, iter.Loop, "iteration should have no loop context when parent has none")
		assert.False(t, iter.IsInLoop(), "iteration should report IsInLoop=false when parent has no loop")
	})
}

// TestMemoization_InheritedProperties tests that ForIteration correctly
// inherits properties from the parent context.
func TestMemoization_InheritedProperties(t *testing.T) {
	t.Parallel()
	t.Run("iteration inherits workflow metadata", func(t *testing.T) {
		parent := NewExecutionContext("wf-parent", "chat-parent", "parent-workflow", "thread-parent").
			WithLoop("loop", 0)

		iter := parent.ForIteration(5, true)

		assert.Equal(t, "wf-parent", iter.WorkflowID, "should inherit WorkflowID")
		assert.Equal(t, "chat-parent", iter.ChatID, "should inherit ChatID")
		assert.Equal(t, "parent-workflow", iter.WorkflowName, "should inherit WorkflowName")
	})

	t.Run("iteration inherits Parent context", func(t *testing.T) {
		parent := NewExecutionContext("wf-child", "chat-456", "agent", "thread-0").
			WithParent("wf-grandparent", "spawn_step").
			WithLoop("loop", 0)

		iter := parent.ForIteration(0, true)

		require.NotNil(t, iter.Parent, "iteration should inherit Parent context")
		assert.Equal(t, "wf-grandparent", iter.Parent.WorkflowID)
		assert.Equal(t, "spawn_step", iter.Parent.StepPath)
	})

	t.Run("iteration inherits model.ThreadMode and ForkedFrom", func(t *testing.T) {
		parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
			WithThreadMode(model.ThreadModeFork, "original-thread").
			WithLoop("loop", 0)

		iter := parent.ForIteration(0, true)

		assert.Equal(t, model.ThreadModeFork, iter.ThreadMode, "should inherit ThreadMode")
		assert.Equal(t, "original-thread", iter.ForkedFrom, "should inherit ForkedFrom")
	})
}
