// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// The sub-workflow both fixtures below reference. Two nodes, so a scenario that
// reaches them proves the body really ran rather than being mocked as a unit.
const transparencyInnerWorkflow = `
name: inner-worker
apiVersion: "1.0"
entry: [call_llm]
outputs:
  response_text: "{{nodes.call_llm.response_text}}"
nodes:
  - id: call_llm
    type: call_llm
    model:
      tags: [fast]
  - id: save
    type: save_message
    args:
      role: "assistant"
      content: "inner"
edges:
  - from: call_llm
    default: save
`

func transparencyLoader(t *testing.T) func(string) (*reliantv1.Workflow, error) {
	t.Helper()
	inner, err := ParseWorkflowYAML([]byte(transparencyInnerWorkflow))
	require.NoError(t, err)
	return func(string) (*reliantv1.Workflow, error) { return inner, nil }
}

func runTransparencyScenario(t *testing.T, workflowYAML string, scenario *Scenario) *ScenarioResult {
	t.Helper()
	wf, err := ParseWorkflowYAML([]byte(workflowYAML))
	require.NoError(t, err)
	return NewEngineWithLoader(wf, transparencyLoader(t)).RunScenario(scenario)
}

// A parallel loop whose inline body contains a `type: workflow` REF — the shape
// of parallel-compete.yaml's `implementations.impl`.
const transparencyRefInParallelLoop = `
name: test-ref-in-parallel-loop
apiVersion: "1.0"
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{[1, 2].map(n, {'num': n})}}"
    key: "{{string(iter.item.num)}}"
    inline:
      entry: [impl]
      outputs:
        response_text: "{{nodes.impl.response_text}}"
      nodes:
        - id: impl
          type: workflow
          ref: builtin://inner-worker
`

// TestRefWorkflowInParallelLoopBodyIsTransparent is the acceptance test for
// bug 1. Before the fix the loop-iteration executors dispatched only on join
// and loop nodes, so a `workflow` ref inside a loop body was ALWAYS opaque:
// events qualified as "fanout.impl.call_llm" could never be consumed no matter
// what the scenario wrote.
func TestRefWorkflowInParallelLoopBodyIsTransparent(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInParallelLoop, &Scenario{
		Name: "ref_in_parallel_loop_body",
		Events: []SimulatedEvent{
			{Node: "fanout.impl.call_llm", Output: map[string]interface{}{"response_text": "candidate 1"}},
			{Node: "fanout.impl.save", Output: map[string]interface{}{}},
			{Node: "fanout.impl.call_llm", Output: map[string]interface{}{"response_text": "candidate 2"}},
			{Node: "fanout.impl.save", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			Reached: []string{"fanout.impl", "fanout.impl.call_llm", "fanout.impl.save"},
		},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.Contains(t, result.Execution.NodesReached, "fanout.impl.call_llm",
		"the ref's internals must execute when the scenario targets them")
}

// TestRefWorkflowInLoopBodyStaysOpaqueWithoutInternalEvents is the other half of
// the gate, and it is load-bearing. A ref the scenario does NOT target must stay
// a black box: descending into an unmocked `builtin://agent` spins, because its
// loop waits on a completion signal no mock supplies.
func TestRefWorkflowInLoopBodyStaysOpaqueWithoutInternalEvents(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInParallelLoop, &Scenario{
		Name: "ref_in_loop_body_opaque",
		Events: []SimulatedEvent{
			{Node: "fanout.impl", Output: map[string]interface{}{"response_text": "mocked 1"}},
			{Node: "fanout.impl", Output: map[string]interface{}{"response_text": "mocked 2"}},
		},
		Expect: &Expectation{
			Outcome:    OutcomeCompleted,
			Reached:    []string{"fanout.impl"},
			NotReached: []string{"fanout.impl.call_llm"},
		},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.NotContains(t, result.Execution.NodesReached, "fanout.impl.call_llm",
		"an unmocked ref must remain a black box")
}

// A sequential (while) loop containing a `workflow` ref — the shape of
// get-it-right.yaml's `attempt.implement`.
const transparencyRefInWhileLoop = `
name: test-ref-in-while-loop
apiVersion: "1.0"
entry: [attempt]
nodes:
  - id: attempt
    type: loop
    while: "false"
    inline:
      entry: [implement]
      outputs:
        response_text: "{{nodes.implement.response_text}}"
      nodes:
        - id: implement
          type: workflow
          ref: builtin://inner-worker
`

func TestRefWorkflowInWhileLoopBodyIsTransparent(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInWhileLoop, &Scenario{
		Name: "ref_in_while_loop_body",
		Events: []SimulatedEvent{
			{Node: "attempt.implement.call_llm", Output: map[string]interface{}{"response_text": "attempt 1"}},
			{Node: "attempt.implement.save", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			Reached: []string{"attempt.implement", "attempt.implement.call_llm"},
		},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
}

// A ref nested inside an INLINE sub-workflow body — the shape of one-ring's
// `planning.plan`.
const transparencyRefInInlineBody = `
name: test-ref-in-inline-body
apiVersion: "1.0"
entry: [planning]
nodes:
  - id: planning
    type: workflow
    inline:
      name: planning-phase
      entry: [plan]
      outputs:
        response_text: "{{nodes.plan.response_text}}"
      nodes:
        - id: plan
          type: workflow
          ref: builtin://inner-worker
`

// TestRefWorkflowInInlineBodyComposesQualifiedIDOnce is the acceptance test for
// bug 2. The nested mocker used to prepend `prefix` while executeWorkflowNode
// composed the full path again from the nodePath it was handed, yielding ids
// like "planning.planning.plan.call_llm" that no event could ever match. The
// id must be composed exactly once.
func TestRefWorkflowInInlineBodyComposesQualifiedIDOnce(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInInlineBody, &Scenario{
		Name: "ref_in_inline_body",
		Events: []SimulatedEvent{
			{Node: "planning.plan.call_llm", Output: map[string]interface{}{"response_text": "the plan"}},
			{Node: "planning.plan.save", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.Contains(t, result.Execution.NodesReached, "planning.plan.call_llm",
		"the composed id must be planning.plan.call_llm, not doubled")
	for _, reached := range result.Execution.NodesReached {
		require.NotContains(t, reached, "planning.planning.",
			"qualified id was composed twice: %s", reached)
	}
	// Non-vacuous: the mock's output really flowed back through the ref.
	require.Equal(t, "the plan",
		result.Execution.NodeOutputs["planning.plan.call_llm"]["response_text"])
}

// TestBlackBoxTrueSuppressesRefWorkflowWarning is the acceptance test for bug 3.
// blackBoxWorkflowWarning consulted only the scenario's qualified-id prefixes
// and never the event's BlackBox flag, so an author who explicitly opted into
// opacity still got warned and had no way to silence it.
func TestBlackBoxTrueSuppressesRefWorkflowWarning(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInInlineBody, &Scenario{
		Name: "ref_black_boxed_explicitly",
		Events: []SimulatedEvent{
			{Node: "planning.plan", BlackBox: true, Output: map[string]interface{}{"response_text": "mocked"}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.NotContains(t, result.Execution.NodesReached, "planning.plan.call_llm",
		"black_box: true must keep the ref opaque")
	requireNoWarningContaining(t, result.Warnings, "black-box sub-workflow")
}

// The other half: opacity the author did NOT ask for still warns. Deliberate
// opacity is silent; accidental opacity is loud.
func TestAccidentalRefOpacityStillWarns(t *testing.T) {
	result := runTransparencyScenario(t, transparencyRefInInlineBody, &Scenario{
		Name: "ref_accidentally_opaque",
		Events: []SimulatedEvent{
			{Node: "planning.plan", Output: map[string]interface{}{"response_text": "mocked"}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	w := requireWarningContaining(t, result.Warnings, "black-box sub-workflow")
	require.Contains(t, w, `"planning.plan"`, "warning must name the node")
}
