// Copyright (c) 2026 Reliant Labs
package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// rc-train's shape: a run node reads the world, a compute node decides, and
// each records what it saw and chose. One node is an activity (observed over
// its mocked result, as the ActivityWrapper does), the other completes in the
// workflow (observed by the RecordNodeObservation local activity), and a loop
// body records once per iteration.
const observedYAML = `
name: observed
inputs:
  lane: {type: string, default: fast}
observe:
  attributes: { lane: "{{ inputs.lane }}" }
entry: [world]
nodes:
  - id: world
    type: run
    timeout: 1m
    args:
      command: "echo behind=0"
    observe:
      attributes: { exit: "{{ output.exit_code }}" }
  - id: plan
    type: compute
    outputs:
      cut: "{{ nodes.world.stdout != 'behind=0' }}"
      hold: "{{ nodes.world.stdout == 'behind=0' ? 'main_unchanged' : '' }}"
    observe:
      events:
        - name: decision
          attributes:
            name: cut
            value: "{{ output.cut ? 'cut' : 'no_cut' }}"
            reason: "{{ output.hold }}"
        - name: broken
          attributes: { x: "{{ output.missing }}" }
  - id: sweep
    type: loop
    while: "iter.iteration < 2"
    items: "{{ ['a', 'b'] }}"
    inline:
      entry: [land]
      nodes:
        - id: land
          type: compute
          outputs: { pr: "{{ iter.item }}" }
          observe:
            events:
              - name: decision
                attributes: { name: land, value: "{{ output.pr == 'a' ? 'merged' : 'skip' }}", pr: "{{ output.pr }}" }
edges:
  - from: world
    default: plan
  - from: plan
    default: sweep
`

func runObserved(t *testing.T, expect *scenario.Expectation) *Result {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(observedYAML))
	require.NoError(t, err)
	return NewRunner(wf).Run(&scenario.Scenario{
		Name: "observed",
		Events: []scenario.SimulatedEvent{
			{Node: "world", Output: map[string]interface{}{"stdout": "behind=0", "exit_code": 0}},
		},
		Expect: expect,
	})
}

func TestScenarioRecordsWhatObserveBlocksRecord(t *testing.T) {
	recorded := true
	res := runObserved(t, &scenario.Expectation{
		Outcome: scenario.OutcomeCompleted,
		Observations: map[string]scenario.ObservationExpectation{
			"world": {Recorded: &recorded, Attributes: map[string]interface{}{"exit": 0, "lane": "fast"}, NoErrors: true},
			"plan": {Events: []scenario.ObservedEventExpectation{
				{Name: "decision", Attributes: map[string]interface{}{"name": "cut", "value": "no_cut", "reason": "main_unchanged"}},
			}},
			"sweep.land": {Events: []scenario.ObservedEventExpectation{
				{Name: "decision", Attributes: map[string]interface{}{"value": "merged", "pr": "a"}},
				{Name: "decision", Attributes: map[string]interface{}{"value": "skip", "pr": "b"}},
			}},
		},
	})
	require.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)

	byNode := map[string][]scenario.RecordedObservation{}
	for _, o := range res.Execution.Observations {
		byNode[o.Node] = append(byNode[o.Node], o)
	}
	require.Len(t, byNode["plan"], 1, "the workflow-side node records once: %+v", res.Execution.Observations)
	assert.Equal(t, "fast", byNode["plan"][0].Attributes["lane"], "the workflow's defaults merge in")
	require.Len(t, byNode["plan"][0].Errors, 1, "a failed expression is reported")
	require.Len(t, byNode["plan"][0].Events, 2)
	assert.Equal(t, "broken", byNode["plan"][0].Events[1].Name)
	assert.NotContains(t, byNode["plan"][0].Events[1].Attributes, "x", "and drops only its own attribute")
	assert.Len(t, byNode["sweep.land"], 2, "a loop body records once per iteration")
	assert.Empty(t, byNode["sweep.land"][0].Attributes, "the defaults do not reach a loop body")
}

// An expectation the run does not meet fails the scenario and names what was
// recorded instead, and the expressions that failed.
func TestScenarioObservationMismatchesNameWhatWasRecorded(t *testing.T) {
	no := false
	res := runObserved(t, &scenario.Expectation{
		Observations: map[string]scenario.ObservationExpectation{
			"plan":  {NoErrors: true, Events: []scenario.ObservedEventExpectation{{Name: "decision", Attributes: map[string]interface{}{"value": "cut"}}}},
			"world": {Recorded: &no},
		},
	})
	require.Equal(t, scenario.StatusFailed, res.Status)
	joined := ""
	for _, m := range res.Mismatches {
		joined += m + "\n"
	}
	assert.Contains(t, joined, `node "plan" recorded no "decision" event with attributes map[value:cut]`)
	assert.Contains(t, joined, "no_cut", "the mismatch shows what was recorded")
	assert.Contains(t, joined, `node "plan"'s observe block had expressions that failed`)
	assert.Contains(t, joined, `expected node "world" to record no observation`)
}

// expect.observations that can never fail is refused before the run.
func TestScenarioObservationExpectationsAreValidated(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: plain
entry: [a]
nodes:
  - id: a
    type: compute
    outputs: { x: "{{ 1 }}" }
  - id: b
    type: compute
    outputs: { y: "{{ 2 }}" }
    observe:
      attributes: { y: "{{ output.y }}" }
edges:
  - from: a
    default: b
`))
	require.NoError(t, err)
	got := scenario.ValidateScenario(&scenario.Scenario{Name: "v", Expect: &scenario.Expectation{
		Observations: map[string]scenario.ObservationExpectation{
			"a":       {NoErrors: true},
			"b":       {Events: []scenario.ObservedEventExpectation{{}}},
			"missing": {NoErrors: true},
		},
	}}, wf)
	joined := ""
	for _, e := range got.Errors {
		joined += e + "\n"
	}
	assert.Contains(t, joined, "expect.observations.a: node never records")
	assert.Contains(t, joined, "expect.observations.b.events[0].name: required")
	assert.Contains(t, joined, `expect.observations: unknown node "missing"`)
}
