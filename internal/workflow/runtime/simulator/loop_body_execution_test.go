package simulator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A parallel loop whose INLINE body references iter.item.<field> in an inner
// node's config. This is the shape that shipped broken in parallel-compete.yaml:
// `iter.item` resolved to a two-key {iteration, index} map, so `iter.item.num`
// failed with "no such key: num" at runtime while the scenario stayed green —
// the simulator replaced the whole body with a scenario literal and therefore
// never compiled the expression that fails.
const workflowParallelLoopInlineBodyUsesIterItem = `
name: test-parallel-inline-iter-item
apiVersion: "1.0"
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{[1, 2].map(n, {'num': n, 'label': 'cand-' + string(n)})}}"
    key: "{{string(iter.item.num)}}"
    inline:
      entry: [make_wt]
      outputs:
        path: "{{nodes.make_wt.path}}"
      nodes:
        - id: make_wt
          type: create_worktree
          args:
            name: "candidate-{{iter.item.num}}"
            base_branch: main
`

// TestParallelLoopInlineBodyIsExecuted is the decisive test for closing the
// black-box blindness. The loop body must actually run and its inner node's
// config must actually be evaluated, so that a bad `iter.item` reference is a
// scenario FAILURE in the fast lane rather than a silent pass.
func TestParallelLoopInlineBodyIsExecuted(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowParallelLoopInlineBodyUsesIterItem)
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "parallel_inline_body_executes",
		Events: []SimulatedEvent{
			{Node: "fanout.make_wt", Output: map[string]interface{}{"path": "/tmp/wt-1", "branch": "b1"}},
			{Node: "fanout.make_wt", Output: map[string]interface{}{"path": "/tmp/wt-2", "branch": "b2"}},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			// The inner node must be REACHED. Under black-box mocking it never
			// is, because the body is never entered.
			Reached: []string{"fanout.make_wt"},
			NodeOutputs: map[string]map[string]interface{}{
				"fanout": {
					"_iterations": 2,
					"_completed":  2,
					"_parallel":   true,
					"_results": map[string]interface{}{
						"1": map[string]interface{}{"path": "/tmp/wt-1"},
						"2": map[string]interface{}{"path": "/tmp/wt-2"},
					},
				},
			},
		},
	}

	result := engine.RunScenario(scenario)
	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
}

// TestParallelLoopInlineBodyCatchesBadIterReference is the negative half: an
// inner node referencing a field that does not exist on iter.item must make the
// scenario FAIL. Before loop bodies were executed this passed green, which is
// exactly how parallel-compete.yaml shipped fully broken.
func TestParallelLoopInlineBodyCatchesBadIterReference(t *testing.T) {
	const badWorkflow = `
name: test-parallel-inline-bad-iter
apiVersion: "1.0"
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{[1, 2].map(n, {'num': n})}}"
    key: "{{string(iter.item.num)}}"
    inline:
      entry: [make_wt]
      nodes:
        - id: make_wt
          type: create_worktree
          args:
            name: "candidate-{{iter.item.nonexistent_field}}"
            base_branch: main
`
	engine, err := NewEngineFromYAML(badWorkflow)
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "parallel_inline_body_bad_iter_ref",
		Events: []SimulatedEvent{
			{Node: "fanout.make_wt", Output: map[string]interface{}{"path": "/tmp/wt-1"}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	}

	result := engine.RunScenario(scenario)
	require.Equal(t, StatusFailed, result.Status,
		"a body referencing a nonexistent iter.item field must fail the scenario; got %s", result.Status)

	joined := strings.Join(result.Mismatches, " | ")
	if result.Execution.Error != nil {
		joined += " | " + result.Execution.Error.Message
	}
	require.Contains(t, joined, "nonexistent_field",
		"the failure must name the offending expression, got: %s", joined)
}

// TestParallelLoopBlackBoxOptOut pins the escape hatch: an author who genuinely
// wants to mock the loop as a unit must still be able to, as an EXPLICIT choice.
func TestParallelLoopBlackBoxOptOut(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowParallelLoopInlineBodyUsesIterItem)
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "parallel_black_box_explicit",
		Events: []SimulatedEvent{
			{Node: "fanout", BlackBox: true, Output: map[string]interface{}{"path": "/mocked/1"}},
			{Node: "fanout", BlackBox: true, Output: map[string]interface{}{"path": "/mocked/2"}},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			// The body was deliberately NOT entered.
			NotReached: []string{"fanout.make_wt"},
			NodeOutputs: map[string]map[string]interface{}{
				"fanout": {
					"_iterations": 2,
					"_completed":  2,
					"_results": map[string]interface{}{
						"1": map[string]interface{}{"path": "/mocked/1"},
						"2": map[string]interface{}{"path": "/mocked/2"},
					},
				},
			},
		},
	}

	result := engine.RunScenario(scenario)
	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
}
