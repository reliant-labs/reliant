// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
)

func TestNewExecutionContext(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	assert.Equal(t, "wf-123", ctx.WorkflowID)
	assert.Equal(t, "chat-456", ctx.ChatID)
	assert.Equal(t, "agent", ctx.WorkflowName)
	assert.Equal(t, "thread-0", ctx.Thread)
	assert.Equal(t, model.ThreadModeNew, ctx.ThreadMode)
	assert.Empty(t, ctx.ForkedFrom)
	assert.Nil(t, ctx.Loop)
	assert.Nil(t, ctx.Parent)
}

func TestExecutionContext_WithThreadMode(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithThreadMode(model.ThreadModeFork, "parent-thread")

	assert.Equal(t, model.ThreadModeFork, ctx.ThreadMode)
	assert.Equal(t, "parent-thread", ctx.ForkedFrom)
}

func TestExecutionContext_WithParent(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithParent("parent-wf", "step-path")

	assert.NotNil(t, ctx.Parent)
	assert.Equal(t, "parent-wf", ctx.Parent.WorkflowID)
	assert.Equal(t, "step-path", ctx.Parent.StepPath)
}

func TestExecutionContext_WithLoop(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithLoop("agent_loop", 5)

	assert.NotNil(t, ctx.Loop)
	assert.Equal(t, "agent_loop", ctx.Loop.NodeID)
	assert.Equal(t, 5, ctx.Loop.Iteration)
}

func TestExecutionContext_ForIteration_StaticKey(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithLoop("agent_loop", 0)

	// reuseThread=true - all iterations share the same thread
	iter0 := parent.ForIteration(0, true)
	iter1 := parent.ForIteration(1, true)
	iter2 := parent.ForIteration(2, true)

	// All should have the same thread
	assert.Equal(t, "thread-0", iter0.Thread)
	assert.Equal(t, "thread-0", iter1.Thread)
	assert.Equal(t, "thread-0", iter2.Thread)

	// Loop context should be set
	assert.NotNil(t, iter1.Loop)
	assert.Equal(t, 1, iter1.Loop.Iteration)
}

func TestExecutionContext_ForChild_Inherit(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	child := parent.ForChild("call_llm", model.ThreadModeInherit, "sub-workflow", true)

	assert.Equal(t, "thread-0", child.Thread) // Same thread
	assert.Equal(t, model.ThreadModeInherit, child.ThreadMode)
	assert.Empty(t, child.ForkedFrom)
	assert.Equal(t, "sub-workflow", child.WorkflowName)
	assert.NotNil(t, child.Parent)
	assert.Equal(t, "wf-123", child.Parent.WorkflowID)
	assert.Equal(t, "call_llm", child.Parent.StepPath)
}

func TestExecutionContext_ForChild_Own(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	child := parent.ForChild("new_thread_step", model.ThreadModeNew, "sub-workflow", true)

	assert.NotEqual(t, "thread-0", child.Thread) // New thread
	assert.Equal(t, model.ThreadModeNew, child.ThreadMode)
	assert.Empty(t, child.ForkedFrom)
}

func TestExecutionContext_ForChild_Fork(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	child := parent.ForChild("fork_step", model.ThreadModeFork, "sub-workflow", true)

	assert.NotEqual(t, "thread-0", child.Thread) // New thread
	assert.Equal(t, model.ThreadModeFork, child.ThreadMode)
	assert.Equal(t, "thread-0", child.ForkedFrom) // Parent thread recorded
}

func TestExecutionContext_ForChild_DeterministicThreads(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	// Same step + mode should produce same thread
	child1 := parent.ForChild("step_a", model.ThreadModeNew, "wf", true)
	child2 := parent.ForChild("step_a", model.ThreadModeNew, "wf", true)

	assert.Equal(t, child1.Thread, child2.Thread)

	// Different step should produce different thread
	child3 := parent.ForChild("step_b", model.ThreadModeNew, "wf", true)
	assert.NotEqual(t, child1.Thread, child3.Thread)
}

func TestExecutionContext_ForChildWorkflow(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	child := parent.ForChildWorkflow("child-wf-789", "spawn_step", model.ThreadModeInherit, "child-workflow", true)

	assert.Equal(t, "child-wf-789", child.WorkflowID) // Different workflow ID
	assert.Equal(t, "chat-456", child.ChatID)         // Same chat
	assert.Equal(t, "thread-0", child.Thread)         // Inherited thread
	assert.NotNil(t, child.Parent)
	assert.Equal(t, "wf-123", child.Parent.WorkflowID)
	assert.Equal(t, "spawn_step", child.Parent.StepPath)
}

func TestExecutionContext_Helpers(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	assert.False(t, ctx.IsInLoop())
	assert.False(t, ctx.IsChildWorkflow())

	ctx.WithLoop("loop", 0)
	assert.True(t, ctx.IsInLoop())

	ctx.WithParent("parent", "step")
	assert.True(t, ctx.IsChildWorkflow())
}

func TestExecutionContext_WithProjectPath(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithProjectPath("/path/to/worktree")

	assert.Equal(t, "/path/to/worktree", ctx.ProjectPath)
	assert.True(t, ctx.HasProjectPath())
}

func TestExecutionContext_HasProjectPath_Empty(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0")

	assert.Empty(t, ctx.ProjectPath)
	assert.False(t, ctx.HasProjectPath())
}

func TestExecutionContext_ForIteration_PropagatesProjectPath(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithProjectPath("/path/to/worktree").
		WithLoop("agent_loop", 0)

	iter := parent.ForIteration(1, true)

	// Project path should be inherited
	assert.Equal(t, "/path/to/worktree", iter.ProjectPath)
	assert.True(t, iter.HasProjectPath())
}

func TestExecutionContext_ForChild_PropagatesProjectPath(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithProjectPath("/path/to/worktree")

	child := parent.ForChild("sub_workflow", model.ThreadModeNew, "sub-workflow", true)

	// Project path should be inherited
	assert.Equal(t, "/path/to/worktree", child.ProjectPath)
	assert.True(t, child.HasProjectPath())
}

func TestExecutionContext_ForChildWorkflow_PropagatesProjectPath(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithProjectPath("/path/to/worktree")

	child := parent.ForChildWorkflow("child-wf-789", "spawn_step", model.ThreadModeInherit, "child-workflow", true)

	// Project path should be inherited
	assert.Equal(t, "/path/to/worktree", child.ProjectPath)
	assert.True(t, child.HasProjectPath())
}

func TestExecutionContext_Clone(t *testing.T) {
	original := NewExecutionContext("wf-123", "chat-456", "agent", "thread-0").
		WithLoop("loop", 5).
		WithParent("parent-wf", "step").
		WithThreadMode(model.ThreadModeFork, "forked-from").
		WithProjectPath("/path/to/worktree")
	original.ParentThread = "parent-thread-0"

	clone := original.Clone()

	// Verify all fields match
	assert.Equal(t, original.WorkflowID, clone.WorkflowID)
	assert.Equal(t, original.ChatID, clone.ChatID)
	assert.Equal(t, original.WorkflowName, clone.WorkflowName)
	assert.Equal(t, original.Thread, clone.Thread)
	assert.Equal(t, original.ThreadMode, clone.ThreadMode)
	assert.Equal(t, original.ForkedFrom, clone.ForkedFrom)
	assert.Equal(t, original.ParentThread, clone.ParentThread)
	assert.Equal(t, original.ProjectPath, clone.ProjectPath)

	assert.Equal(t, original.Loop.NodeID, clone.Loop.NodeID)
	assert.Equal(t, original.Loop.Iteration, clone.Loop.Iteration)

	assert.Equal(t, original.Parent.WorkflowID, clone.Parent.WorkflowID)

	// Verify deep copy (modifying clone doesn't affect original)
	clone.Thread = "modified"
	clone.Loop.NodeID = "modified-loop"

	assert.Equal(t, "thread-0", original.Thread)
	assert.Equal(t, "loop", original.Loop.NodeID)
}

func TestForChild_MemoFalse_InLoop_Fork(t *testing.T) {
	t.Parallel()
	parent := &ExecutionContext{
		WorkflowID: "wf-1",
		Thread:     "parent-thread",
		Loop:       &ExecLoopContext{NodeID: "loop1", Iteration: 0},
	}

	child0 := parent.ForChild("step", model.ThreadModeFork, "child", false)

	parent.Loop.Iteration = 1
	child1 := parent.ForChild("step", model.ThreadModeFork, "child", false)

	parent.Loop.Iteration = 2
	child2 := parent.ForChild("step", model.ThreadModeFork, "child", false)

	// Each iteration should produce a different thread
	assert.NotEqual(t, child0.Thread, child1.Thread)
	assert.NotEqual(t, child1.Thread, child2.Thread)
	assert.NotEqual(t, child0.Thread, child2.Thread)

	// All should fork from the same parent
	assert.Equal(t, "parent-thread", child0.ForkedFrom)
	assert.Equal(t, "parent-thread", child1.ForkedFrom)
}

func TestForChild_MemoTrue_InLoop_Fork(t *testing.T) {
	t.Parallel()
	parent := &ExecutionContext{
		WorkflowID: "wf-1",
		Thread:     "parent-thread",
		Loop:       &ExecLoopContext{NodeID: "loop1", Iteration: 0},
	}

	child0 := parent.ForChild("step", model.ThreadModeFork, "child", true)

	parent.Loop.Iteration = 1
	child1 := parent.ForChild("step", model.ThreadModeFork, "child", true)

	// Memoized — same thread across iterations
	assert.Equal(t, child0.Thread, child1.Thread)
}

func TestForChild_MemoFalse_NotInLoop(t *testing.T) {
	t.Parallel()
	parent := &ExecutionContext{
		WorkflowID: "wf-1",
		Thread:     "parent-thread",
		// No Loop context
	}

	child := parent.ForChild("step", model.ThreadModeFork, "child", false)
	child2 := parent.ForChild("step", model.ThreadModeFork, "child", false)

	// Without loop context, memo has no effect — same key inputs = same thread
	assert.Equal(t, child.Thread, child2.Thread)
}

func TestForChild_MemoFalse_InLoop_New(t *testing.T) {
	t.Parallel()
	parent := &ExecutionContext{
		WorkflowID: "wf-1",
		Thread:     "parent-thread",
		Loop:       &ExecLoopContext{NodeID: "loop1", Iteration: 0},
	}

	child0 := parent.ForChild("step", model.ThreadModeNew, "child", false)

	parent.Loop.Iteration = 1
	child1 := parent.ForChild("step", model.ThreadModeNew, "child", false)

	assert.NotEqual(t, child0.Thread, child1.Thread)
}

func TestForChild_MemoFalse_InLoop_Inherit(t *testing.T) {
	t.Parallel()
	parent := &ExecutionContext{
		WorkflowID: "wf-1",
		Thread:     "parent-thread",
		Loop:       &ExecLoopContext{NodeID: "loop1", Iteration: 0},
	}

	child0 := parent.ForChild("step", model.ThreadModeInherit, "child", false)

	parent.Loop.Iteration = 1
	child1 := parent.ForChild("step", model.ThreadModeInherit, "child", false)

	// Inherit always uses parent thread — memo has no effect
	assert.Equal(t, child0.Thread, child1.Thread)
	assert.Equal(t, "parent-thread", child0.Thread)
}
