// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"

	"github.com/stretchr/testify/assert"
)

func TestThreadMode_Constants(t *testing.T) {
	// Verify the string values match the spec
	assert.Equal(t, "new", model.ThreadModeNew)
	assert.Equal(t, "inherit", model.ThreadModeInherit)
	assert.Equal(t, "fork", model.ThreadModeFork)
}

// ============================================================================
// INLINE WORKFLOW EXECUTOR EXECUTION CONTEXT TESTS
// ============================================================================

func TestInlineWorkflowExecutor_ExecContext(t *testing.T) {
	t.Run("WithExecContext sets execution context", func(t *testing.T) {
		// Note: We can only test the struct-level behavior without Temporal context
		executor := &InlineWorkflowExecutor{
			nodeID: "test-node",
		}

		execCtx := &ExecutionContext{
			Thread:     "thread-123",
			ThreadMode: model.ThreadModeNew,
		}

		updated := executor.WithExecContext(execCtx)

		assert.Same(t, executor, updated) // Should return same instance
		assert.NotNil(t, executor.execContext)
		assert.Equal(t, "thread-123", executor.execContext.Thread)
		assert.Equal(t, model.ThreadModeNew, executor.execContext.ThreadMode)
	})

	t.Run("ExecContext with inherit mode", func(t *testing.T) {
		execCtx := &ExecutionContext{
			Thread:     "parent-thread",
			ThreadMode: model.ThreadModeInherit,
		}

		executor := &InlineWorkflowExecutor{
			nodeID:      "loop-body",
			execContext: execCtx,
		}

		assert.Equal(t, model.ThreadModeInherit, executor.execContext.ThreadMode)
		assert.Equal(t, "parent-thread", executor.execContext.Thread)
	})

	t.Run("ExecContext with fork mode includes forked_from", func(t *testing.T) {
		execCtx := &ExecutionContext{
			Thread:     "forked-thread-456",
			ThreadMode: model.ThreadModeFork,
			ForkedFrom: "parent-thread-123",
		}

		executor := &InlineWorkflowExecutor{
			nodeID:      "parallel-agent",
			execContext: execCtx,
		}

		assert.Equal(t, model.ThreadModeFork, executor.execContext.ThreadMode)
		assert.Equal(t, "forked-thread-456", executor.execContext.Thread)
		assert.Equal(t, "parent-thread-123", executor.execContext.ForkedFrom)
	})

	t.Run("ExecContext with loop context", func(t *testing.T) {
		execCtx := &ExecutionContext{
			Thread:     "loop-thread-iter-3",
			ThreadMode: model.ThreadModeNew,
			Loop: &ExecLoopContext{
				NodeID:    "batch-loop",
				Iteration: 3,
			},
		}

		executor := &InlineWorkflowExecutor{
			nodeID:        "batch-processor",
			loopIteration: 3,
			execContext:   execCtx,
		}

		assert.NotNil(t, executor.execContext.Loop)
		assert.Equal(t, 3, executor.execContext.Loop.Iteration)
		assert.Equal(t, 3, executor.loopIteration)
	})
}

// NOTE: TestResolveWorkflowNodeName removed - the resolveWorkflowNodeName function
// and the v3 types it used no longer exist. Workflow name resolution is now handled
// by the core semantics system via SubWorkflowContract.WorkflowIdentity.
