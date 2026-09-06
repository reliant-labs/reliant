// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// `iter.item` must resolve inside a loop body. docs/workflows/patterns.mdx:436
// states the contract plainly: "Inside the loop body, `iter.item` is the current
// element, `iter.index` is its position (0-indexed), and `iter.key` is the map
// key when iterating a map."
//
// The contract held only for `workflow` and `loop` nodes, which build their own
// iteration context before evaluating config. Every ORDINARY node — call_llm,
// run, create_worktree — routes through StepExecutor.Start, which rebuilt the
// context from the iteration counter alone (model.BuildIterContext) and dropped
// item and key on the floor. `iter.item.num` then failed to compile with
// "no such key: num" against a two-key map holding only iteration and index.
//
// These tests pin the contract at the node types that were broken. They are
// written against the real DynamicWorkflow, so they fail on the actual defect
// rather than on a mock's idea of it.

// parallelItemBindingYAML: a parallel loop whose body is an ORDINARY node
// (call_llm) reading iter.item.num. This is the shape of
// internal/workflow/builtin/parallel-compete.yaml:151, where a create_worktree
// node names itself "compete-impl-{{iter.item.num}}".
const parallelItemBindingYAML = `
name: parallel-item-binding
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{[1, 2, 3].map(n, {'num': n})}}"
    key: "{{string(iter.item.num)}}"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "candidate {{iter.item.num}}"
`

// sequentialItemBindingYAML: the SAME ordinary-node body under a SEQUENTIAL
// items loop. Sequential loops resolve items and keys through the identical
// helper (loop_executor.go buildIterCtx), so if the defect is in the shared
// StepExecutor seam rather than in the parallel executor, this fails too.
//
// A sequential loop requires an explicit `while` (loop_executor.go:131); the
// items list still drives auto-stop, so the guard here is only a ceiling.
const sequentialItemBindingYAML = `
name: sequential-item-binding
entry: [each]
nodes:
  - id: each
    type: loop
    while: "iter.iteration < 2"
    items: "{{[1, 2].map(n, {'num': n})}}"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "candidate {{iter.item.num}}"
`

// TestParallelLoop_OrdinaryNodeSeesIterItem is the parallel-compete regression.
// Before the fix the body never ran: create_wt's config evaluation raised
// "CEL evaluation error: no such key: num" on every iteration, all three
// iterations exhausted their retries, and the loop produced no results.
func TestParallelLoop_OrdinaryNodeSeesIterItem(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "parallel_item_binding",
		Events: []simulator.SimulatedEvent{
			{Node: "fanout.work", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "fanout.work", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "fanout.work", Output: map[string]interface{}{"response_text": "ok"}},
		},
	}
	res := runYAMLScenario(t, parallelItemBindingYAML, sc)
	t.Logf("parallel: status=%s outcome=%s reached=%v mismatches=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Mismatches)

	require.Contains(t, res.Execution.NodesReached, "fanout.work",
		"an ordinary node in a parallel loop body must resolve iter.item and run")
	require.NotEqual(t, "error", res.Execution.Outcome,
		"the loop must not fail; iter.item.num is the documented contract")
}

// TestSequentialLoop_OrdinaryNodeSeesIterItem pins the same contract on the
// sequential path. Both paths funnel ordinary nodes through StepExecutor, so
// this is not a parallel-only defect — the asymmetry is workflow/loop nodes
// versus ordinary nodes, not parallel versus sequential.
func TestSequentialLoop_OrdinaryNodeSeesIterItem(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "sequential_item_binding",
		Events: []simulator.SimulatedEvent{
			{Node: "each.work", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "each.work", Output: map[string]interface{}{"response_text": "ok"}},
		},
	}
	res := runYAMLScenario(t, sequentialItemBindingYAML, sc)
	t.Logf("sequential: status=%s outcome=%s reached=%v mismatches=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached, res.Mismatches)

	require.Contains(t, res.Execution.NodesReached, "each.work",
		"an ordinary node in a sequential items loop must resolve iter.item and run")
	require.NotEqual(t, "error", res.Execution.Outcome,
		"the loop must not fail; iter.item.num is the documented contract")
}
