// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// ==========================================================================
// UNIT TESTS - ChildWorkflowTracker thread input registry (no Temporal needed)
// ==========================================================================

func TestChildWorkflowTracker_RegisterThreadInputs(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs := map[string]interface{}{"model": "gpt-4", "temperature": 0.7}
	tracker.RegisterThreadInputs("thread-abc", inputs, nil)

	got := tracker.GetThreadInputs("thread-abc")
	assert.Equal(t, inputs, got)
	// Must be same map reference - mutating one affects the other
	got["model"] = "changed"
	assert.Equal(t, "changed", inputs["model"])
}

func TestChildWorkflowTracker_GetThreadInputs_Unregistered(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	got := tracker.GetThreadInputs("nonexistent")
	assert.Nil(t, got)
}

func TestChildWorkflowTracker_GetThreadInputs_NilMap(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}
	// threadInputs is nil by default
	got := tracker.GetThreadInputs("anything")
	assert.Nil(t, got)
}

func TestChildWorkflowTracker_UnregisterThreadInputs(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs := map[string]interface{}{"model": "claude"}
	tracker.RegisterThreadInputs("thread-1", inputs, nil)
	assert.NotNil(t, tracker.GetThreadInputs("thread-1"))

	tracker.UnregisterThreadInputs("thread-1")
	assert.Nil(t, tracker.GetThreadInputs("thread-1"))
}

func TestChildWorkflowTracker_MultipleThreads(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs1 := map[string]interface{}{"model": "gpt-4"}
	inputs2 := map[string]interface{}{"model": "claude", "mode": "fast"}

	tracker.RegisterThreadInputs("thread-a", inputs1, nil)
	tracker.RegisterThreadInputs("thread-b", inputs2, nil)

	assert.Equal(t, "gpt-4", tracker.GetThreadInputs("thread-a")["model"])
	assert.Equal(t, "claude", tracker.GetThreadInputs("thread-b")["model"])
	assert.Equal(t, "fast", tracker.GetThreadInputs("thread-b")["mode"])
	assert.Nil(t, tracker.GetThreadInputs("thread-c"))
}

func TestChildWorkflowTracker_GetAllThreadInputs(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs1 := map[string]interface{}{"a": 1}
	inputs2 := map[string]interface{}{"b": 2}
	tracker.RegisterThreadInputs("t1", inputs1, nil)
	tracker.RegisterThreadInputs("t2", inputs2, nil)

	all := tracker.GetAllThreadInputs()
	assert.Len(t, all, 2)
	assert.Equal(t, inputs1, all["t1"])
	assert.Equal(t, inputs2, all["t2"])
}

func TestChildWorkflowTracker_SharedPointerMutation(t *testing.T) {
	t.Parallel()
	// Verify that mutating through the registry mutates the original map
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs := map[string]interface{}{"model": "gpt-4"}
	tracker.RegisterThreadInputs("thread-1", inputs, nil)

	// Mutate through the registry
	got := tracker.GetThreadInputs("thread-1")
	got["model"] = "claude"

	// Original map should reflect the change
	assert.Equal(t, "claude", inputs["model"])
}

// ==========================================================================
// WORKFLOW TESTS - Signal handler + query handler (Temporal test suite)
// ==========================================================================

type ThreadInputsSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestThreadInputsSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ThreadInputsSuite))
}

// testWorkflowWithThreadInputs is a minimal workflow that sets up
// the signal handler and query handlers, then waits for a done signal.
func testWorkflowWithThreadInputs(ctx workflow.Context) error {
	rootInputs := map[string]interface{}{
		"model":       "gpt-4",
		"temperature": 0.7,
	}

	childTracker := &ChildWorkflowTracker{
		children: make(map[string]bool),
	}

	// Register a thread with its own inputs
	threadInputs := map[string]interface{}{
		"model":       "claude",
		"temperature": 0.9,
		"mode":        "research",
	}
	childTracker.RegisterThreadInputs("thread-abc", threadInputs, nil)

	// Register a second thread
	thread2Inputs := map[string]interface{}{
		"model": "gemini",
	}
	childTracker.RegisterThreadInputs("thread-def", thread2Inputs, nil)

	// A spawned agent running under a preset that pins its own model. "model"
	// is owned by the thread, so a global update must not overwrite it.
	presetThreadInputs := map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"moderate"}},
		"mode":  "auto",
	}
	childTracker.RegisterThreadInputs("thread-preset", presetThreadInputs,
		map[string]bool{"model": true})

	// Set up query handler for root inputs
	if err := workflow.SetQueryHandler(ctx, "get_workflow_inputs", func() (map[string]interface{}, error) {
		return rootInputs, nil
	}); err != nil {
		return err
	}

	// Set up query handler for per-thread inputs
	if err := workflow.SetQueryHandler(ctx, "get_thread_inputs", func(thread string) (map[string]interface{}, error) {
		if ti := childTracker.GetThreadInputs(thread); ti != nil {
			return ti, nil
		}
		return rootInputs, nil
	}); err != nil {
		return err
	}

	// Set up signal handler (same as production code)
	doneCh := workflow.NewChannel(ctx)
	setupInputUpdateHandler(ctx, rootInputs, childTracker, "test-wf-id", doneCh)

	// Wait for done signal
	doneCh.Receive(ctx, nil)
	return nil
}

func (s *ThreadInputsSuite) TestQueryThreadInputs_ReturnsThreadSpecificInputs() {
	env := s.NewTestWorkflowEnvironment()

	// Query thread inputs after workflow starts
	env.RegisterDelayedCallback(func() {
		// Query for registered thread
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var inputs map[string]interface{}
		s.NoError(result.Get(&inputs))
		s.Equal("claude", inputs["model"])
		s.Equal("research", inputs["mode"])

		// Query for second thread
		result2, err := env.QueryWorkflow("get_thread_inputs", "thread-def")
		s.NoError(err)
		var inputs2 map[string]interface{}
		s.NoError(result2.Get(&inputs2))
		s.Equal("gemini", inputs2["model"])

		// Send done signal to end workflow
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"__done": true})
	}, 100*time.Millisecond)

	// Send done to exit the workflow
	env.RegisterDelayedCallback(func() {
		// Need to send on the internal done channel - use cancellation instead
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
	// Workflow won't complete on its own since we can't send to doneCh from outside,
	// but the queries should have executed. Cancel it.
	env.CancelWorkflow()
}

func (s *ThreadInputsSuite) TestQueryThreadInputs_FallsBackToRootForUnknownThread() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Query for unregistered thread should fall back to root inputs
		result, err := env.QueryWorkflow("get_thread_inputs", "nonexistent-thread")
		s.NoError(err)
		var inputs map[string]interface{}
		s.NoError(result.Get(&inputs))
		s.Equal("gpt-4", inputs["model"])
		s.Equal(0.7, inputs["temperature"])

		env.CancelWorkflow()
	}, 100*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

func (s *ThreadInputsSuite) TestSignal_ThreadScopedUpdate() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Send thread-scoped signal
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"__thread":    "thread-abc",
			"temperature": 0.5,
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		// Query thread inputs - should have updated temperature
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var inputs map[string]interface{}
		s.NoError(result.Get(&inputs))
		s.Equal(0.5, inputs["temperature"])
		// Model should be unchanged
		s.Equal("claude", inputs["model"])

		// Root inputs should NOT have been modified
		rootResult, err := env.QueryWorkflow("get_workflow_inputs")
		s.NoError(err)
		var rootInputs map[string]interface{}
		s.NoError(rootResult.Get(&rootInputs))
		s.Equal(0.7, rootInputs["temperature"]) // unchanged
		s.Equal("gpt-4", rootInputs["model"])   // unchanged

		// Other thread should NOT have been modified
		result2, err := env.QueryWorkflow("get_thread_inputs", "thread-def")
		s.NoError(err)
		var inputs2 map[string]interface{}
		s.NoError(result2.Get(&inputs2))
		s.Equal("gemini", inputs2["model"]) // unchanged

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

func (s *ThreadInputsSuite) TestSignal_GlobalUpdate_PropagatesToThreadInputs() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Send global signal (no __thread key)
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"model": "o1-preview",
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		// Root inputs should be updated
		rootResult, err := env.QueryWorkflow("get_workflow_inputs")
		s.NoError(err)
		var rootInputs map[string]interface{}
		s.NoError(rootResult.Get(&rootInputs))
		s.Equal("o1-preview", rootInputs["model"])

		// Thread inputs SHOULD now reflect the global update
		threadResult, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var threadInputs map[string]interface{}
		s.NoError(threadResult.Get(&threadInputs))
		s.Equal("o1-preview", threadInputs["model"]) // propagated

		// Second thread should also reflect the global update
		thread2Result, err := env.QueryWorkflow("get_thread_inputs", "thread-def")
		s.NoError(err)
		var thread2Inputs map[string]interface{}
		s.NoError(thread2Result.Get(&thread2Inputs))
		s.Equal("o1-preview", thread2Inputs["model"]) // propagated

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

// A global param update must not overwrite an input the thread set explicitly
// for itself. Regression test for preset-pinned models being silently replaced
// by the root chat's model: the `implementer` preset pins model tags
// [moderate] (claude-5-sonnet), but every user message re-sent the root's own
// model tags [flagship], which the global fan-out copied into every registered
// child — so spawned agents started on sonnet and flipped mid-run to
// claude-5-opus, carrying opus's adaptive thinking and effort settings.
func (s *ThreadInputsSuite) TestSignal_GlobalUpdate_DoesNotOverwriteThreadOwnedInput() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// The root chat re-broadcasts its own model on every message.
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"model": map[string]interface{}{"tags": []interface{}{"flagship"}},
			"mode":  "plan",
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-preset")
		s.NoError(err)
		var threadInputs map[string]interface{}
		s.NoError(result.Get(&threadInputs))

		// The preset's pinned model must survive the global update.
		s.Equal(
			map[string]interface{}{"tags": []interface{}{"moderate"}},
			threadInputs["model"],
			"preset-owned model must not be overwritten by the root's global update",
		)

		// A key the thread does NOT own still tracks the global update.
		s.Equal("plan", threadInputs["mode"],
			"non-owned keys should still receive global updates")

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

// A thread-scoped update targets the thread deliberately, so it MUST still be
// able to change an owned key — only the blind global fan-out is held back.
func (s *ThreadInputsSuite) TestSignal_ThreadScopedUpdate_OverridesOwnedInput() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"__thread": "thread-preset",
			"model":    map[string]interface{}{"id": "claude-5-opus"},
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-preset")
		s.NoError(err)
		var threadInputs map[string]interface{}
		s.NoError(result.Get(&threadInputs))
		s.Equal(
			map[string]interface{}{"id": "claude-5-opus"},
			threadInputs["model"],
			"an explicit thread-scoped update must override an owned key",
		)
		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

func (s *ThreadInputsSuite) TestSignal_ThreadScopedUpdate_UnregisteredThread() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Send thread-scoped signal for a thread that doesn't exist
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"__thread":    "nonexistent",
			"temperature": 0.1,
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		// Root inputs should NOT have been modified (it was thread-scoped, not global)
		rootResult, err := env.QueryWorkflow("get_workflow_inputs")
		s.NoError(err)
		var rootInputs map[string]interface{}
		s.NoError(rootResult.Get(&rootInputs))
		s.Equal(0.7, rootInputs["temperature"]) // unchanged

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

func (s *ThreadInputsSuite) TestSignal_ThreadScopedUpdate_AddsNewKey() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Add a key that didn't exist before on the thread
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"__thread":  "thread-abc",
			"new_param": "new_value",
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var inputs map[string]interface{}
		s.NoError(result.Get(&inputs))
		s.Equal("new_value", inputs["new_param"])
		// Existing keys should still be there
		s.Equal("claude", inputs["model"])

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

// ==========================================================================
// NEW TESTS - Global propagation, thread-scoped isolation, inline map sharing
// ==========================================================================

func (s *ThreadInputsSuite) TestSignal_GlobalUpdate_PreservesThreadOnlyKeys() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Send global signal that adds a new key
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"ask": false,
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		// Root inputs should have the new key
		rootResult, err := env.QueryWorkflow("get_workflow_inputs")
		s.NoError(err)
		var rootInputs map[string]interface{}
		s.NoError(rootResult.Get(&rootInputs))
		s.Equal(false, rootInputs["ask"])

		// Thread-abc should have ask AND retain its own keys (mode, model)
		threadResult, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var threadInputs map[string]interface{}
		s.NoError(threadResult.Get(&threadInputs))
		s.Equal(false, threadInputs["ask"])
		s.Equal("claude", threadInputs["model"])  // thread-specific key preserved
		s.Equal("research", threadInputs["mode"]) // thread-specific key preserved

		// Thread-def should also have ask and retain its own model
		thread2Result, err := env.QueryWorkflow("get_thread_inputs", "thread-def")
		s.NoError(err)
		var thread2Inputs map[string]interface{}
		s.NoError(thread2Result.Get(&thread2Inputs))
		s.Equal(false, thread2Inputs["ask"])
		s.Equal("gemini", thread2Inputs["model"]) // thread-specific key preserved

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

func (s *ThreadInputsSuite) TestSignal_ThreadScopedUpdate_DoesNotAffectOtherThreads() {
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		// Send thread-scoped signal only for thread-abc
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"__thread":    "thread-abc",
			"temperature": 0.1,
			"new_key":     "only-for-abc",
		})
	}, 100*time.Millisecond)

	env.RegisterDelayedCallback(func() {
		// thread-abc should have the updates
		result, err := env.QueryWorkflow("get_thread_inputs", "thread-abc")
		s.NoError(err)
		var inputs map[string]interface{}
		s.NoError(result.Get(&inputs))
		s.Equal(0.1, inputs["temperature"])
		s.Equal("only-for-abc", inputs["new_key"])

		// thread-def should NOT have any of those updates
		result2, err := env.QueryWorkflow("get_thread_inputs", "thread-def")
		s.NoError(err)
		var inputs2 map[string]interface{}
		s.NoError(result2.Get(&inputs2))
		s.Nil(inputs2["temperature"])       // thread-def never had temperature
		s.Nil(inputs2["new_key"])           // should not have leaked
		s.Equal("gemini", inputs2["model"]) // unchanged

		// Root inputs should NOT have been modified
		rootResult, err := env.QueryWorkflow("get_workflow_inputs")
		s.NoError(err)
		var rootInputs map[string]interface{}
		s.NoError(rootResult.Get(&rootInputs))
		s.Equal(0.7, rootInputs["temperature"]) // unchanged
		s.Nil(rootInputs["new_key"])            // not leaked

		env.CancelWorkflow()
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testWorkflowWithThreadInputs)
}

// TestChildWorkflowTracker_GlobalPropagation_Unit verifies that mutating all thread
// inputs via GetAllThreadInputs (as the signal handler does) works correctly.
func TestChildWorkflowTracker_GlobalPropagation_Unit(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs1 := map[string]interface{}{"model": "claude", "mode": "research"}
	inputs2 := map[string]interface{}{"model": "gemini"}
	tracker.RegisterThreadInputs("t1", inputs1, nil)
	tracker.RegisterThreadInputs("t2", inputs2, nil)

	// Simulate global propagation: apply update to all thread inputs
	update := map[string]interface{}{"ask": false}
	for _, threadInputs := range tracker.GetAllThreadInputs() {
		for key, value := range update {
			threadInputs[key] = value
		}
	}

	// Both threads should have the new key
	assert.Equal(t, false, inputs1["ask"])
	assert.Equal(t, false, inputs2["ask"])

	// Existing keys should be preserved
	assert.Equal(t, "claude", inputs1["model"])
	assert.Equal(t, "research", inputs1["mode"])
	assert.Equal(t, "gemini", inputs2["model"])
}

// TestChildWorkflowTracker_GlobalPropagation_DoesNotAffectUnregistered verifies that
// unregistered threads are not affected by global propagation.
func TestChildWorkflowTracker_GlobalPropagation_DoesNotAffectUnregistered(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}

	inputs1 := map[string]interface{}{"model": "claude"}
	tracker.RegisterThreadInputs("t1", inputs1, nil)

	// Unregister t1, then propagate
	tracker.UnregisterThreadInputs("t1")

	update := map[string]interface{}{"ask": false}
	for _, threadInputs := range tracker.GetAllThreadInputs() {
		for key, value := range update {
			threadInputs[key] = value
		}
	}

	// Original map should NOT have been touched (thread was unregistered)
	assert.Nil(t, inputs1["ask"])
}

// TestInlineInheritedSubWorkflow_SharesParentMap verifies that inline-inherited
// sub-workflows get the exact same map pointer as the parent, so signal updates
// to the parent are visible immediately without any propagation.
func TestInlineInheritedSubWorkflow_SharesParentMap(t *testing.T) {
	t.Parallel()
	parentInputs := map[string]interface{}{"model": "gpt-4", "ask": true}

	executor := &InlineWorkflowExecutor{
		workflowInputs: parentInputs,
		invocationContract: &core.SubWorkflowContract{
			InputPolicy: core.InputPolicyInlineInheritParentInputs,
		},
	}

	subInputs := executor.buildSubWorkflowInputs()

	// Must be the exact same pointer
	assert.Equal(t, fmt.Sprintf("%p", parentInputs), fmt.Sprintf("%p", subInputs),
		"inline-inherited sub-workflow should share parent map reference")

	// Mutations to parent should be visible via subInputs
	parentInputs["ask"] = false
	assert.Equal(t, false, subInputs["ask"])

	// Mutations to subInputs should be visible via parent
	subInputs["new_key"] = "value"
	assert.Equal(t, "value", parentInputs["new_key"])
}
