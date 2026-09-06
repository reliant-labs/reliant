// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// A node's identity in a scenario is its fully-qualified dotted graph path, and
// that path must be COMPOSED at every nesting boundary.
//
// The runtime could not express this before RuntimeContext.NodePath existed.
// Identity was derived from LoopNodeID, which is LOOP-SCOPED — a single id
// persisted to the loop_node_id column under a unique index over
// (workflow_id, loop_node_id, loop_iteration) — and the sub-workflow boundary
// deliberately keeps the OUTERMOST id rather than composing
// (inline_workflow_executor.go's stepLoopNodeID). So one level down a node
// reported "outer.node", and two levels down it reported "outer.node" as well:
// the middle segment was simply lost, and two different nodes could collide on
// one id.
//
// Fixing that by dotting loop_node_id was rejected: it would change what a
// production DB column MEANS to satisfy a test harness. NodePath is a separate,
// unpersisted field, and these tests are the evidence it composes correctly.

// nestedPathYAML is the case the old code could not express: a loop whose body
// is a sub-workflow whose body holds the node under test.
//
//	impl_loop (loop)
//	  └── attempt (workflow, inline)
//	        └── review (call_llm)   → "impl_loop.attempt.review"
//
// Deriving identity from the single loop id yields "impl_loop.review" here —
// the `attempt` segment vanishes.
const nestedPathYAML = `
name: nested-node-path
entry: [impl_loop]
nodes:
  - id: impl_loop
    type: loop
    while: "iter.iteration < 1"
    inline:
      entry: [attempt]
      nodes:
        - id: attempt
          type: workflow
          inline:
            entry: [review]
            nodes:
              - id: review
                type: call_llm
                args:
                  system_prompt: "review the work"
`

// TestNodePath_ThreeLevelsDeep_ReportsComposedPath is the acceptance evidence.
func TestNodePath_ThreeLevelsDeep_ReportsComposedPath(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "nested_node_path",
		Events: []simulator.SimulatedEvent{
			{Node: "impl_loop.attempt.review", Output: map[string]interface{}{"response_text": "looks good"}},
		},
	}
	res := runYAMLScenario(t, nestedPathYAML, sc)
	t.Logf("status=%s outcome=%s reached=%v mismatches=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Mismatches)

	require.Contains(t, res.Execution.NodesReached, "impl_loop.attempt.review",
		"a node inside a sub-workflow inside a loop must report the fully composed path")
	require.NotContains(t, res.Execution.NodesReached, "impl_loop.review",
		"the middle sub-workflow segment must not be dropped — that is the bug this pins")
	require.NotContains(t, res.Execution.NodesReached, "review",
		"a nested node must never report its bare id")
}

// fourLevelPathYAML nests one level further — a loop inside the sub-workflow —
// so the path has to survive loop → sub-workflow → loop → node. Nothing in the
// composition is special-cased per boundary type, and this is what proves it.
//
//	outer_loop (loop)
//	  └── stage (workflow, inline)
//	        └── inner_loop (loop, inline)
//	              └── work (call_llm)  → "outer_loop.stage.inner_loop.work"
const fourLevelPathYAML = `
name: four-level-node-path
entry: [outer_loop]
nodes:
  - id: outer_loop
    type: loop
    while: "iter.iteration < 1"
    inline:
      entry: [stage]
      nodes:
        - id: stage
          type: workflow
          inline:
            entry: [inner_loop]
            nodes:
              - id: inner_loop
                type: loop
                while: "iter.iteration < 1"
                inline:
                  entry: [work]
                  nodes:
                    - id: work
                      type: call_llm
                      args:
                        system_prompt: "do the work"
`

func TestNodePath_FourLevelsDeep_ComposesThroughEveryBoundary(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "four_level_node_path",
		Events: []simulator.SimulatedEvent{
			{Node: "outer_loop.stage.inner_loop.work", Output: map[string]interface{}{"response_text": "done"}},
		},
	}
	res := runYAMLScenario(t, fourLevelPathYAML, sc)
	t.Logf("status=%s outcome=%s reached=%v mismatches=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Mismatches)

	require.Contains(t, res.Execution.NodesReached, "outer_loop.stage.inner_loop.work",
		"the path must compose through loop -> sub-workflow -> loop -> node")
}

// TestNodePath_TopLevelNodeIsBareID pins the other end of the convention: a node
// with nothing above it reports its own id, with no leading separator.
func TestNodePath_TopLevelNodeIsBareID(t *testing.T) {
	const topLevelYAML = `
name: top-level-node-path
entry: [solo]
nodes:
  - id: solo
    type: call_llm
    args:
      system_prompt: "hello"
`
	sc := &simulator.Scenario{
		Name: "top_level_node_path",
		Events: []simulator.SimulatedEvent{
			{Node: "solo", Output: map[string]interface{}{"response_text": "hi"}},
		},
	}
	res := runYAMLScenario(t, topLevelYAML, sc)
	t.Logf("status=%s reached=%v", res.Status, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "solo")
}
