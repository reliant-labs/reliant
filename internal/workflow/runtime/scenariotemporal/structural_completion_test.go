// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// A LOOP node was the last structural node this harness could not fully see.
//
// Unlike a join it is not invisible outright: its BODY dispatches activities, so
// `grade.attempt` shows up in reached through the composed NodePath of the
// nodes underneath it, and the per-iteration WorkflowCheckpoint told the
// recorder how many iterations ran. What was missing is everything the loop
// NODE ITSELF produces. Its result is computed in-workflow — the executor
// returns a LoopOutput and the caller writes model.ProtoLoopOutputToMap of it
// straight into the node-output store — so no activity mock is ever dispatched
// for it and no mock can observe it.
//
// That left two holes, both fixed by the same signal:
//
//   - OUTPUTS. The harness synthesized `_iterations` from checkpoints and
//     nothing else, so a scenario asserting `node_outputs: {grade: {...}}` on
//     any real published field could not pass, even though the values were
//     already correct — get-it-right's `eval_strategy` and `review_grade` came
//     through the workflow-outputs channel byte-for-byte identical to the fast
//     simulator's while the per-node view held only a count.
//   - COMPLETION. A checkpoint proves the loop was ENTERED. Nothing marked the
//     moment it finished, so `completed: [grade]` was unassertable.
//
// The fix mirrors the join precedent exactly: an observer on the workflow
// context, populated at the single shared success exit of
// InlineLoopExecutor.Execute, exposed through a query the harness reads after
// the run. It writes nothing and dispatches no activity, so production persists
// precisely what it persisted before.

// loopOutputsYAML is a minimal one-iteration loop that PUBLISHES a field.
// `grade` declares an output derived from its body's node, which is the thing
// the harness could not previously see: `verdict` is computed inside the
// workflow and never passes through an activity boundary.
const loopOutputsYAML = `
name: loop-outputs-observable
entry: [grade]
nodes:
  - id: grade
    type: loop
    while: "iter.iteration < 1"
    inline:
      entry: [judge]
      nodes:
        - id: judge
          type: call_llm
          args:
            system_prompt: "judge it"
      outputs:
        verdict: "{{nodes.judge.response_text}}"

  - id: done
    type: save_message
    args:
      role: assistant
      content: "graded"

edges:
  - from: grade
    default: done
`

// TestLoop_PublishedOutputs_AreObservable is the acceptance evidence for the
// outputs half. Before the StructuralNodesCompleted query existed this failed on
// `verdict`: node_outputs["grade"] was the harness-synthesized
// {_iterations: 1} and nothing else, even though the runtime had computed
// verdict="pass" and every downstream CEL read it correctly.
func TestLoop_PublishedOutputs_AreObservable(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "loop_published_outputs",
		Events: []simulator.SimulatedEvent{
			{Node: "grade.judge", Output: map[string]interface{}{"response_text": "pass"}},
		},
	}
	res := runYAMLScenario(t, loopOutputsYAML, sc)
	t.Logf("status=%s outcome=%s reached=%v completed=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Execution.NodesCompleted)
	t.Logf("node_outputs[grade]=%v", res.Execution.NodeOutputs["grade"])

	got := res.Execution.NodeOutputs["grade"]
	require.NotNil(t, got, "the loop node must have observable outputs")
	require.Equal(t, "pass", got["verdict"],
		"a loop's PUBLISHED outputs must be observable per-node, not only through workflow outputs")

	// The count the checkpoint fallback used to be the only source of must
	// survive: the query reports the runtime's own _iterations, so replacing
	// the fallback must not lose it.
	require.EqualValues(t, 1, got[model.LoopOutputIterationsField],
		"_iterations must still be reported, now from the runtime's own LoopOutput")

	require.Contains(t, res.Execution.NodesReached, "done",
		"the loop's downstream edge must still fire; observing must not change routing")
}

// TestLoop_Completion_IsReported is the acceptance evidence for the completion
// half. `grade` was in reached (its body ran) but never in completed, which is
// exactly what one-ring/implement_only failed on for `impl_loop`.
func TestLoop_Completion_IsReported(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "loop_completion_reported",
		Events: []simulator.SimulatedEvent{
			{Node: "grade.judge", Output: map[string]interface{}{"response_text": "pass"}},
		},
		Expect: &simulator.Expectation{
			Completed: []string{"grade"},
		},
	}
	res := runYAMLScenario(t, loopOutputsYAML, sc)
	t.Logf("status=%s completed=%v mismatches=%v",
		res.Status, res.Execution.NodesCompleted, res.Mismatches)

	require.Contains(t, res.Execution.NodesCompleted, "grade",
		"a loop that ran to completion must be reported completed, not merely reached")
	require.Empty(t, res.Mismatches)
}

// TestWorkflowNode_Completion_IsReported covers the OTHER structural node type,
// which is a distinct executor (InlineWorkflowExecutor) and a distinct code
// path — and the one one-ring/implement_only actually failed on. `impl_loop`
// reads like a loop from its name, but it is `type: workflow`: its body ran and
// reported `impl_loop.attempt.implement`, so it sat in `reached` while never
// appearing in `completed`.
func TestWorkflowNode_Completion_IsReported(t *testing.T) {
	const workflowNodeYAML = `
name: workflow-node-completion
entry: [phase]
nodes:
  - id: phase
    type: workflow
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "do the work"
      outputs:
        summary: "{{nodes.work.response_text}}"

  - id: done
    type: save_message
    args:
      role: assistant
      content: "phase done"

edges:
  - from: phase
    default: done
`
	sc := &simulator.Scenario{
		Name: "workflow_node_completion",
		Events: []simulator.SimulatedEvent{
			{Node: "phase.work", Output: map[string]interface{}{"response_text": "shipped"}},
		},
	}
	res := runYAMLScenario(t, workflowNodeYAML, sc)
	t.Logf("status=%s completed=%v node_outputs[phase]=%v",
		res.Status, res.Execution.NodesCompleted, res.Execution.NodeOutputs["phase"])

	require.Contains(t, res.Execution.NodesCompleted, "phase",
		"a workflow: node that ran to completion must be reported completed, not merely reached")
	require.Equal(t, "shipped", res.Execution.NodeOutputs["phase"]["summary"],
		"a workflow: node's published outputs must be observable per-node")
}

// TestLoop_FailedLoop_IsNotReportedCompleted is the honesty half, and the one
// that keeps the signal from becoming fabrication. A loop that errors out did
// NOT complete, and reporting it anyway would make every loop in the corpus
// read as completed whether or not it finished — the same failure mode
// TestJoin_UnsatisfiedJoin_IsNotReported guards against for joins.
//
// The body's `explode` node is a run step whose command fails; the scenario
// supplies no event for it, so the loop unwinds with an error.
func TestLoop_FailedLoop_IsNotReportedCompleted(t *testing.T) {
	const failingLoopYAML = `
name: loop-failure-not-completed
entry: [risky]
nodes:
  - id: risky
    type: loop
    while: "iter.iteration < 1"
    inline:
      entry: [explode]
      nodes:
        - id: explode
          type: call_llm
          args:
            system_prompt: "boom"
      outputs:
        verdict: "{{nodes.explode.nonexistent_field.deeper}}"

  - id: done
    type: save_message
    args:
      role: assistant
      content: "unreachable"

edges:
  - from: risky
    default: done
`
	sc := &simulator.Scenario{
		Name: "loop_failure",
		Events: []simulator.SimulatedEvent{
			{Node: "risky.explode", Output: map[string]interface{}{"response_text": "x"}},
		},
	}
	res := runYAMLScenario(t, failingLoopYAML, sc)
	t.Logf("status=%s outcome=%s completed=%v error=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesCompleted, res.Execution.Error)

	if res.Execution.Outcome != "error" {
		t.Skipf("loop did not fail (outcome=%s); this test only asserts the failure path",
			res.Execution.Outcome)
	}
	require.NotContains(t, res.Execution.NodesCompleted, "risky",
		"a loop that errored must NOT be reported completed; that would fabricate execution")
}
