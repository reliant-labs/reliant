// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// A JOIN node is the last structural node that was invisible to this harness.
//
// It runs no activity: DynamicWorkflow's dispatch loop filters join steps out
// entirely (workflow.go, "Skip join steps - they are handled by
// processJoinEvents"), because a join's whole job is to be satisfied by its
// SOURCES completing rather than to execute anything itself. So no activity
// mock could observe it, and `reached: [verify]` — which the fast simulator
// reports, since it appends a satisfied join to visitedSteps — was unassertable
// on this lane.
//
// The two escape hatches that worked for other structural nodes do not apply.
// A loop announces itself through WorkflowCheckpoint, but a checkpoint means
// "an interrupted run resumes HERE" and a join is not a resume position — it is
// satisfied by state the sources already wrote, so resuming into it would
// re-enter a node with nothing to run. And a `workflow` node is recovered from
// the composed NodePath of the real nodes underneath it; a join has no nodes
// underneath it, and its ancestors appear in no path any executor builds.
//
// The honest fix is therefore a signal the runtime did not previously emit:
// JoinSatisfied, dispatched at the one place production already decides a join
// fired (processJoinEvents, guarded by MarkTriggered so it is exactly once).
// It persists nothing — it is the join's twin of the SkippedStep activity,
// which exists for precisely the same reason: a node that performs no work
// still has to be visible.

// parallelJoinYAML is the shape of parallel-compete's `verify` node. `type:
// parallel` is sugar: it desugars into the branch nodes plus a JOIN that waits
// on them (yaml/sugar.go), so `verify` is a join node with two sources.
const parallelJoinYAML = `
name: parallel-join-reached
entry: [start]
nodes:
  - id: start
    type: call_llm
    args:
      system_prompt: "kick off"

  - id: verify
    type: parallel
    branches:
      - id: verify_test
        type: run
        command: "true"
      - id: verify_build
        type: run
        command: "true"

  - id: done
    type: save_message
    args:
      role: assistant
      content: "verified"

edges:
  - from: start
    default: verify
  - from: verify
    default: done
`

// TestJoin_TopLevel_IsReported is the acceptance evidence. Before the
// JoinSatisfied signal existed this failed on the `verify` assertion: both
// branches ran, `done` ran (so the join demonstrably fired), and the join
// itself appeared nowhere in reached.
func TestJoin_TopLevel_IsReported(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "parallel_join_reached",
		Events: []simulator.SimulatedEvent{
			{Node: "start", Output: map[string]interface{}{"response_text": "go"}},
			{Node: "verify_test", Output: map[string]interface{}{"exit_code": 0}},
			{Node: "verify_build", Output: map[string]interface{}{"exit_code": 0}},
		},
	}
	res := runYAMLScenario(t, parallelJoinYAML, sc)
	t.Logf("status=%s outcome=%s reached=%v mismatches=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Mismatches)

	require.Contains(t, res.Execution.NodesReached, "verify_test")
	require.Contains(t, res.Execution.NodesReached, "verify_build")
	require.Contains(t, res.Execution.NodesReached, "verify",
		"a satisfied join must be reported as reached — its sources completing IS its execution")
	require.Contains(t, res.Execution.NodesReached, "done",
		"the join's downstream edge must still fire; the signal must not change routing")
}

// TestJoin_UnsatisfiedJoin_IsNotReported is the other half of the contract, and
// the half that keeps the signal honest. A join whose sources have not all
// completed has NOT been reached, and reporting it anyway would be exactly the
// fabrication this package must not commit: the harness would claim execution
// the runtime never performed, and every join in the corpus would then read as
// reached whether or not the graph got there.
//
// `gate` waits on two sources under "all", and only `fast` ever completes —
// `never` sits behind an edge that is never taken, because `start`'s only case
// routes to `fast`. So the join's satisfaction branch never runs, and nothing
// should report it.
func TestJoin_UnsatisfiedJoin_IsNotReported(t *testing.T) {
	const unsatisfiedJoinYAML = `
name: unsatisfied-join
entry: [start]
nodes:
  - id: start
    type: call_llm
    args:
      system_prompt: "kick off"

  - id: fast
    type: call_llm
    args:
      system_prompt: "fast branch"

  - id: never
    type: call_llm
    args:
      system_prompt: "never runs"

  - id: gate
    type: join
    condition: "all"

edges:
  - from: start
    cases:
      - to: fast
        condition: "true"
      - to: never
        condition: "false"
  - from: fast
    default: gate
  - from: never
    default: gate
`
	sc := &simulator.Scenario{
		Name: "unsatisfied_join",
		Events: []simulator.SimulatedEvent{
			{Node: "start", Output: map[string]interface{}{"response_text": "go"}},
			{Node: "fast", Output: map[string]interface{}{"response_text": "done"}},
		},
	}
	res := runYAMLScenario(t, unsatisfiedJoinYAML, sc)
	t.Logf("status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "fast")
	require.NotContains(t, res.Execution.NodesReached, "never",
		"the second source must not run — that is what leaves the join unsatisfied")
	require.NotContains(t, res.Execution.NodesReached, "gate",
		"a join that was never satisfied must NOT be reported reached; "+
			"reporting it would fabricate execution the runtime did not perform")
}
