// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Thread Mode Inherit Tests
// =============================================================================

func TestThreadModeInherit_ChildGetsSameThreadAsParent(t *testing.T) {
	t.Parallel()
	// Create parent context with a specific thread
	parent := NewExecutionContext("wf-parent-123", "chat-456", "parent-workflow", "parent-thread-uuid")

	// Create child with inherit mode
	child := parent.ForChild("child_step", model.ThreadModeInherit, "child-workflow", true)

	// Child should have the exact same thread as parent
	assert.Equal(t, "parent-thread-uuid", child.Thread)
	assert.Equal(t, parent.Thread, child.Thread)
}

func TestThreadModeInherit_ThreadModeIsSetCorrectly(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("step", model.ThreadModeInherit, "child", true)

	assert.Equal(t, model.ThreadModeInherit, child.ThreadMode)
}

func TestThreadModeInherit_ForkedFromIsEmpty(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("step", model.ThreadModeInherit, "child", true)

	// Inherit mode should not set ForkedFrom
	assert.Empty(t, child.ForkedFrom)
}

func TestThreadModeInherit_ParentContextIsSet(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-parent-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("my_child_step", model.ThreadModeInherit, "child-wf", true)

	require.NotNil(t, child.Parent)
	assert.Equal(t, "wf-parent-123", child.Parent.WorkflowID)
	assert.Equal(t, "my_child_step", child.Parent.StepPath)
}

func TestThreadModeInherit_MultipleChildrenShareSameThread(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "shared-thread")

	child1 := parent.ForChild("step_a", model.ThreadModeInherit, "wf-a", true)
	child2 := parent.ForChild("step_b", model.ThreadModeInherit, "wf-b", true)
	child3 := parent.ForChild("step_c", model.ThreadModeInherit, "wf-c", true)

	// All children should share the parent's thread
	assert.Equal(t, "shared-thread", child1.Thread)
	assert.Equal(t, "shared-thread", child2.Thread)
	assert.Equal(t, "shared-thread", child3.Thread)
}

func TestThreadModeInherit_NestedInheritance(t *testing.T) {
	t.Parallel()
	// Create a chain: grandparent -> parent -> child
	grandparent := NewExecutionContext("wf-gp", "chat", "gp", "original-thread")
	parent := grandparent.ForChild("parent_step", model.ThreadModeInherit, "parent", true)
	child := parent.ForChild("child_step", model.ThreadModeInherit, "child", true)

	// All should share the original thread
	assert.Equal(t, "original-thread", grandparent.Thread)
	assert.Equal(t, "original-thread", parent.Thread)
	assert.Equal(t, "original-thread", child.Thread)
}

// =============================================================================
// Thread Mode New Tests
// =============================================================================

func TestThreadModeNew_ChildGetsNewDeterministicThread(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "parent-thread")

	child := parent.ForChild("new_thread_step", model.ThreadModeNew, "child-workflow", true)

	// Child should have a different thread from parent
	assert.NotEqual(t, "parent-thread", child.Thread)
	// Thread should not be empty
	assert.NotEmpty(t, child.Thread)
}

func TestThreadModeNew_ThreadModeIsSetCorrectly(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("step", model.ThreadModeNew, "child", true)

	assert.Equal(t, model.ThreadModeNew, child.ThreadMode)
}

func TestThreadModeNew_ForkedFromIsEmpty(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("step", model.ThreadModeNew, "child", true)

	// New mode should not set ForkedFrom (it's a fresh thread)
	assert.Empty(t, child.ForkedFrom)
}

func TestThreadModeNew_DeterministicThreadID_SameInputsSameOutput(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	// Call ForChild multiple times with the same parameters
	child1 := parent.ForChild("deterministic_step", model.ThreadModeNew, "child", true)
	child2 := parent.ForChild("deterministic_step", model.ThreadModeNew, "child", true)

	// Should produce the same thread ID (deterministic)
	assert.Equal(t, child1.Thread, child2.Thread)
}

func TestThreadModeNew_DeterministicThreadID_DifferentStepIDsDifferentThreads(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child1 := parent.ForChild("step_alpha", model.ThreadModeNew, "child", true)
	child2 := parent.ForChild("step_beta", model.ThreadModeNew, "child", true)

	// Different step IDs should produce different threads
	assert.NotEqual(t, child1.Thread, child2.Thread)
}

func TestThreadModeNew_DeterministicThreadID_DifferentParentWorkflowIDsDifferentThreads(t *testing.T) {
	t.Parallel()
	parent1 := NewExecutionContext("wf-111", "chat-456", "parent", "thread-0")
	parent2 := NewExecutionContext("wf-222", "chat-456", "parent", "thread-0")

	child1 := parent1.ForChild("same_step", model.ThreadModeNew, "child", true)
	child2 := parent2.ForChild("same_step", model.ThreadModeNew, "child", true)

	// Different parent workflow IDs should produce different threads
	assert.NotEqual(t, child1.Thread, child2.Thread)
}

func TestThreadModeNew_MultipleChildrenGetUniqueThreads(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "shared-thread")

	child1 := parent.ForChild("step_a", model.ThreadModeNew, "wf-a", true)
	child2 := parent.ForChild("step_b", model.ThreadModeNew, "wf-b", true)
	child3 := parent.ForChild("step_c", model.ThreadModeNew, "wf-c", true)

	// All children should have unique threads
	assert.NotEqual(t, child1.Thread, child2.Thread)
	assert.NotEqual(t, child2.Thread, child3.Thread)
	assert.NotEqual(t, child1.Thread, child3.Thread)

	// All should be different from parent
	assert.NotEqual(t, parent.Thread, child1.Thread)
	assert.NotEqual(t, parent.Thread, child2.Thread)
	assert.NotEqual(t, parent.Thread, child3.Thread)
}

// =============================================================================
// Thread Mode Fork Tests
// =============================================================================

func TestThreadModeFork_ChildGetsNewThread(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "parent-thread")

	child := parent.ForChild("fork_step", model.ThreadModeFork, "child-workflow", true)

	// Child should have a different thread from parent
	assert.NotEqual(t, "parent-thread", child.Thread)
	assert.NotEmpty(t, child.Thread)
}

func TestThreadModeFork_ThreadModeIsSetCorrectly(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("step", model.ThreadModeFork, "child", true)

	assert.Equal(t, model.ThreadModeFork, child.ThreadMode)
}

func TestThreadModeFork_ForkedFromIsSetToParentThread(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "the-parent-thread")

	child := parent.ForChild("fork_step", model.ThreadModeFork, "child", true)

	// Fork mode should record the parent's thread in ForkedFrom
	assert.Equal(t, "the-parent-thread", child.ForkedFrom)
	assert.Equal(t, parent.Thread, child.ForkedFrom)
}

func TestThreadModeFork_DeterministicThreadID(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child1 := parent.ForChild("fork_step", model.ThreadModeFork, "child", true)
	child2 := parent.ForChild("fork_step", model.ThreadModeFork, "child", true)

	// Should produce the same thread ID (deterministic)
	assert.Equal(t, child1.Thread, child2.Thread)
}

func TestThreadModeFork_DifferentStepIDsDifferentThreads(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0")

	child1 := parent.ForChild("fork_alpha", model.ThreadModeFork, "child", true)
	child2 := parent.ForChild("fork_beta", model.ThreadModeFork, "child", true)

	// Different step IDs should produce different forked threads
	assert.NotEqual(t, child1.Thread, child2.Thread)
}

func TestThreadModeFork_ParentContextIsSet(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-parent-123", "chat-456", "parent", "thread-0")

	child := parent.ForChild("my_fork_step", model.ThreadModeFork, "child-wf", true)

	require.NotNil(t, child.Parent)
	assert.Equal(t, "wf-parent-123", child.Parent.WorkflowID)
	assert.Equal(t, "my_fork_step", child.Parent.StepPath)
}

func TestThreadModeFork_MultipleForks(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "parent-thread")

	// Fork multiple times (e.g., parallel reviewers)
	reviewer1 := parent.ForChild("reviewer_1", model.ThreadModeFork, "reviewer", true)
	reviewer2 := parent.ForChild("reviewer_2", model.ThreadModeFork, "reviewer", true)
	reviewer3 := parent.ForChild("reviewer_3", model.ThreadModeFork, "reviewer", true)

	// All should have different threads
	assert.NotEqual(t, reviewer1.Thread, reviewer2.Thread)
	assert.NotEqual(t, reviewer2.Thread, reviewer3.Thread)

	// All should record the same parent thread as forked from
	assert.Equal(t, "parent-thread", reviewer1.ForkedFrom)
	assert.Equal(t, "parent-thread", reviewer2.ForkedFrom)
	assert.Equal(t, "parent-thread", reviewer3.ForkedFrom)
}

// =============================================================================
// DeterministicThread Function Tests
// =============================================================================

func TestDeterministicThread_SameInputsSameOutput(t *testing.T) {
	t.Parallel()
	thread1 := DeterministicThread("wf-123", "some-key")
	thread2 := DeterministicThread("wf-123", "some-key")

	assert.Equal(t, thread1, thread2)
}

func TestDeterministicThread_DifferentInputsDifferentOutput(t *testing.T) {
	t.Parallel()
	thread1 := DeterministicThread("wf-123", "key-a")
	thread2 := DeterministicThread("wf-123", "key-b")

	assert.NotEqual(t, thread1, thread2)
}

func TestDeterministicThread_DifferentWorkflowIDsDifferentOutput(t *testing.T) {
	t.Parallel()
	thread1 := DeterministicThread("wf-111", "same-key")
	thread2 := DeterministicThread("wf-222", "same-key")

	assert.NotEqual(t, thread1, thread2)
}

func TestDeterministicThread_OutputIsValidUUID(t *testing.T) {
	t.Parallel()
	thread := DeterministicThread("wf-123", "test-key")

	// Should be a valid UUID format (36 characters with hyphens)
	assert.Len(t, thread, 36)
	assert.Contains(t, thread, "-")
}

func TestDeterministicThread_ConsistentAcrossMultipleCalls(t *testing.T) {
	t.Parallel()
	parentID := "wf-test-workflow-12345"
	key := "step_call_llm:iter:5"

	results := make([]string, 100)
	for i := 0; i < 100; i++ {
		results[i] = DeterministicThread(parentID, key)
	}

	// All results should be identical
	for i := 1; i < 100; i++ {
		assert.Equal(t, results[0], results[i], "Call %d should match call 0", i)
	}
}

func TestDeterministicThread_EmptyInputs(t *testing.T) {
	t.Parallel()
	// Should handle empty strings gracefully
	thread1 := DeterministicThread("", "key")
	thread2 := DeterministicThread("wf", "")
	thread3 := DeterministicThread("", "")

	// Should still produce valid UUIDs
	assert.Len(t, thread1, 36)
	assert.Len(t, thread2, 36)
	assert.Len(t, thread3, 36)

	// Different inputs (even empty ones) should produce different outputs
	assert.NotEqual(t, thread1, thread2)
	assert.NotEqual(t, thread2, thread3)
}

// =============================================================================
// Default Thread Mode Tests
// =============================================================================

func TestThreadMode_DefaultFallsBackToInherit(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "parent-thread")

	// Using an empty/invalid model.ThreadMode should default to inherit
	child := parent.ForChild("step", "", "child", true)

	assert.Equal(t, "parent-thread", child.Thread)
}

// =============================================================================
// ForChild with Loop Context Tests
// =============================================================================

func TestForChild_InheritsLoopContext(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-123", "chat-456", "parent", "thread-0").
		WithLoop("agent_loop", 5)

	child := parent.ForChild("child_step", model.ThreadModeInherit, "child", true)

	// Loop context should be inherited
	require.NotNil(t, child.Loop)
	assert.Equal(t, "agent_loop", child.Loop.NodeID)
	assert.Equal(t, 5, child.Loop.Iteration)
}

// =============================================================================
// ForChildWorkflow Tests
// =============================================================================

func TestForChildWorkflow_SetsChildWorkflowID(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-parent", "chat-456", "parent", "thread-0")

	child := parent.ForChildWorkflow("wf-child-specific", "spawn_step", model.ThreadModeInherit, "child-workflow", true)

	assert.Equal(t, "wf-child-specific", child.WorkflowID)
}

func TestForChildWorkflow_TracksParentWorkflowID(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-parent", "chat-456", "parent", "thread-0")

	child := parent.ForChildWorkflow("wf-child", "spawn_step", model.ThreadModeInherit, "child", true)

	require.NotNil(t, child.Parent)
	assert.Equal(t, "wf-parent", child.Parent.WorkflowID)
	assert.Equal(t, "spawn_step", child.Parent.StepPath)
}

func TestForChildWorkflow_WithForkMode(t *testing.T) {
	t.Parallel()
	parent := NewExecutionContext("wf-parent", "chat-456", "parent", "parent-thread")

	child := parent.ForChildWorkflow("wf-child", "fork_step", model.ThreadModeFork, "child", true)

	// Should have forked thread
	assert.NotEqual(t, "parent-thread", child.Thread)
	assert.Equal(t, "parent-thread", child.ForkedFrom)
	assert.Equal(t, "wf-child", child.WorkflowID)
}

// =============================================================================
// Thread Mode Integration Tests
// =============================================================================

func TestThreadModes_TypicalParallelReviewerPattern(t *testing.T) {
	t.Parallel()
	// Simulates: main workflow -> fork to multiple reviewers -> each has own thread
	main := NewExecutionContext("wf-main", "chat", "implement-validate", "main-thread")

	// Implementer inherits the main thread
	implementer := main.ForChild("implementer", model.ThreadModeInherit, "agent", true)
	assert.Equal(t, "main-thread", implementer.Thread)

	// After implementation, fork to multiple reviewers
	// Each reviewer sees the conversation up to this point
	reviewer1 := main.ForChild("reviewer_1", model.ThreadModeFork, "reviewer", true)
	reviewer2 := main.ForChild("reviewer_2", model.ThreadModeFork, "reviewer", true)

	// Reviewers have their own threads
	assert.NotEqual(t, main.Thread, reviewer1.Thread)
	assert.NotEqual(t, main.Thread, reviewer2.Thread)
	assert.NotEqual(t, reviewer1.Thread, reviewer2.Thread)

	// But they know they forked from main
	assert.Equal(t, "main-thread", reviewer1.ForkedFrom)
	assert.Equal(t, "main-thread", reviewer2.ForkedFrom)
}

func TestThreadModes_TypicalDevilsAdvocatePattern(t *testing.T) {
	t.Parallel()
	// Simulates: main debate with advocates in fresh threads
	debate := NewExecutionContext("wf-debate", "chat", "devils-advocate", "debate-thread")

	// Main speaker uses the debate thread
	speaker := debate.ForChild("speaker", model.ThreadModeInherit, "agent", true)
	assert.Equal(t, "debate-thread", speaker.Thread)

	// Devil's advocate gets a fresh thread (no context from main debate)
	devil := debate.ForChild("advocate", model.ThreadModeNew, "agent", true)
	assert.NotEqual(t, "debate-thread", devil.Thread)
	assert.Empty(t, devil.ForkedFrom) // Not a fork, fresh thread

	// Judge forks to see the full debate context
	judge := debate.ForChild("judge", model.ThreadModeFork, "judge-agent", true)
	assert.NotEqual(t, "debate-thread", judge.Thread)
	assert.Equal(t, "debate-thread", judge.ForkedFrom)
}

func TestThreadModes_NestedWorkflowScenario(t *testing.T) {
	t.Parallel()
	// Simulates: parent -> child (inherit) -> grandchild (fork)
	parent := NewExecutionContext("wf-parent", "chat", "parent-wf", "root-thread")

	// Child inherits parent's thread
	child := parent.ForChild("child_step", model.ThreadModeInherit, "child-wf", true)
	assert.Equal(t, "root-thread", child.Thread)

	// Grandchild forks from child (which is the same as root)
	grandchild := child.ForChild("grandchild_step", model.ThreadModeFork, "grandchild-wf", true)
	assert.NotEqual(t, "root-thread", grandchild.Thread)
	assert.Equal(t, "root-thread", grandchild.ForkedFrom)

	// Another grandchild with new thread
	grandchild2 := child.ForChild("grandchild_new", model.ThreadModeNew, "grandchild2-wf", true)
	assert.NotEqual(t, "root-thread", grandchild2.Thread)
	assert.NotEqual(t, grandchild.Thread, grandchild2.Thread)
	assert.Empty(t, grandchild2.ForkedFrom)
}

// =============================================================================
// Thread Constants Tests
// =============================================================================

func TestThreadModeConstants(t *testing.T) {
	t.Parallel()
	// Verify the string values of thread mode constants
	assert.Equal(t, "inherit", model.ThreadModeInherit)
	assert.Equal(t, "new", model.ThreadModeNew)
	assert.Equal(t, "fork", model.ThreadModeFork)
}
