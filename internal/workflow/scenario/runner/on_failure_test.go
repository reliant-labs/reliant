// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `work` is an inline workflow node whose args cannot be evaluated: an error
// the engine returns up the run, which is what fails it. (A step that merely
// exhausts its retries PAUSES the run at the root, so it is not a failure.)
const onFailureYAML = `
name: guarded
inputs:
  m: {type: object, default: {}}
  boom: {type: boolean, default: true}
entry: [prep]
nodes:
  - id: prep
    type: compute
    outputs:
      n: "{{ 1 }}"
  - id: work
    type: workflow
    condition: "inputs.boom"
    args:
      x: "{{ inputs.m.missing.deeper }}"
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: compute
          outputs:
            y: "{{ 1 }}"
  - id: after
    type: save_message
    args:
      role: assistant
      content: "done"
edges:
  - from: prep
    default: work
  - from: work
    default: after
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        display_style: warning
        content: "failed at {{ failure.node_id }} ({{ failure.node_path }}, {{ failure.error_code }}, attempt {{ failure.attempt }}); prep n={{ nodes.?prep.n.orValue(-1) }}; after={{ has(nodes.after) }}"
`

func TestOnFailure_RunsOnceWithTheFailureContext(t *testing.T) {
	res := runYAMLScenario(t, onFailureYAML, &scenario.Scenario{Name: "fails"})
	require.Equal(t, "error", res.Execution.Outcome, "the handler does not mask the failure: %+v", res.Execution.Error)

	got := savedContent(res, "on_failure.note")
	require.Len(t, got, 1, "the handler runs exactly once; saved=%+v on_failure=%+v", res.Execution.SavedMessages, res.Execution.OnFailure)
	assert.Contains(t, got[0], "failed at work (work, node_error, attempt 1)")
	assert.Contains(t, got[0], "prep n=1", "the handler reads the outputs the run had produced")
	assert.Contains(t, got[0], "after=false", "and sees nodes that never ran as absent")
	assert.NotContains(t, res.Execution.NodesCompleted, "after")

	require.NotNil(t, res.Execution.OnFailure)
	assert.True(t, res.Execution.OnFailure.Ran)
	assert.Contains(t, res.Execution.OnFailure.Error, "missing")
}

func TestOnFailure_NotRunWhenTheRunCompletes(t *testing.T) {
	res := runYAMLScenario(t, onFailureYAML, &scenario.Scenario{
		Name:   "ok",
		Inputs: map[string]interface{}{"boom": false},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Empty(t, savedContent(res, "on_failure.note"), "a run that did not fail never runs the handler")
	require.NotNil(t, res.Execution.OnFailure)
	assert.False(t, res.Execution.OnFailure.Ran)
}

const onFailureOutcomeYAML = `
name: verdict
entry: [check]
nodes:
  - id: check
    type: compute
    outputs:
      ok: "{{ false }}"
  - id: gave_up
    type: save_message
    outcome: failure
    args:
      role: assistant
      content: "gave up"
  - id: fine
    type: save_message
    args:
      role: assistant
      content: "fine"
edges:
  - from: check
    cases:
      - to: [fine]
        condition: "nodes.check.ok == true"
      - to: [gave_up]
        condition: "true"
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "{{ failure.error_code }} at {{ failure.node_id }}: {{ failure.error }}"
`

// A run that routes to a node declaring `outcome: failure` returns no error and
// still FAILED: the handler runs for the verdict.
func TestOnFailure_RunsForADeclaredFailureOutcome(t *testing.T) {
	res := runYAMLScenario(t, onFailureOutcomeYAML, &scenario.Scenario{
		Name: "verdict",
		Expect: &scenario.Expectation{
			Outcome: "completed",
			OnFailure: &scenario.OnFailureExpectation{
				Ran: boolPtr(true), NodeID: "gave_up", ErrorCode: "outcome",
			},
			Messages: map[string]scenario.MessageExpectation{
				"on_failure.note": {Count: intPtr(1), ContentContains: []string{"outcome at gave_up"}},
			},
		},
	})
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
}

func TestOnFailure_ExpectationsMismatchIsReported(t *testing.T) {
	res := runYAMLScenario(t, onFailureOutcomeYAML, &scenario.Scenario{
		Name: "wrong",
		Expect: &scenario.Expectation{
			OnFailure: &scenario.OnFailureExpectation{Ran: boolPtr(false), NodeID: "other"},
		},
	})
	assert.EqualValues(t, "failed", res.Status)
	assert.Len(t, res.Mismatches, 1+1-1+1, "%v", res.Mismatches)
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

func TestOnFailurePathPrefixMatchesRuntime(t *testing.T) {
	wf := runtimeOnFailureNodeID()
	assert.Equal(t, "on_failure", wf)
}

func runtimeOnFailureNodeID() string { return runtime.OnFailureNodeID }

// A handler that errors does not change the verdict and is not itself handled:
// the run ends failed exactly as it would without one, and the error is
// recorded for the scenario.
func TestOnFailure_HandlerErrorIsRecordedAndNeverMasksTheFailure(t *testing.T) {
	src := `
name: bad-handler
inputs:
  m: {type: object, default: {}}
entry: [work]
nodes:
  - id: work
    type: workflow
    args:
      x: "{{ inputs.m.missing.deeper }}"
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: compute
          outputs:
            y: "{{ 1 }}"
on_failure:
  entry: [broken]
  nodes:
    - id: broken
      type: compute
      outputs:
        z: "{{ inputs.m.nope.deeper }}"
    - id: never
      type: save_message
      args:
        role: assistant
        content: "unreachable"
  edges:
    - from: broken
      default: never
`
	res := runYAMLScenario(t, src, &scenario.Scenario{
		Name: "bad",
		Expect: &scenario.Expectation{
			Outcome:   "error",
			OnFailure: &scenario.OnFailureExpectation{Ran: boolPtr(true), NodeID: "work", HandlerFailed: boolPtr(true)},
		},
	})
	assert.EqualValues(t, "passed", res.Status, "%v / %+v", res.Mismatches, res.Execution.OnFailure)
	assert.Empty(t, savedContent(res, "on_failure.never"))
}

func TestOnFailure_AbsentHandlerObservesNothing(t *testing.T) {
	res := runYAMLScenario(t, computeYAML, &scenario.Scenario{Name: "c",
		Events: []scenario.SimulatedEvent{{Node: "parse", Output: map[string]interface{}{"stdout": "{}", "exit_code": 0}}}})
	assert.Nil(t, res.Execution.OnFailure)
}
