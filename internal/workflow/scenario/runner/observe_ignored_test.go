// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every observe expression below fails if anything evaluates it
// (inputs.m.missing.deeper is a "no such key"), and `nodes.*` is not even in
// observe's scope. This release only parses and stores observe: the runtime
// must not resolve it with the node's config (the Cel* resolver) or anywhere
// else, so the run is exactly the run without it.
const observeIgnoredYAML = `
name: observe-ignored
inputs:
  m: {type: object, default: {}}
observe:
  attributes: { boom: "{{ inputs.m.missing.deeper }}" }
  metrics:
    - name: node.duration
      kind: histogram
      value: "{{ nodes.plan.cut }}"
entry: [parse]
nodes:
  - id: parse
    type: run
    args:
      command: "echo ok"
    observe:
      attributes: { exit: "{{ inputs.m.missing.deeper }}" }
  - id: plan
    type: compute
    outputs:
      cut: "{{ nodes.parse.exit_code == 0 }}"
    observe:
      events:
        - name: decision
          when: inputs.m.missing.deeper
          each: "{{ inputs.m.missing.deeper }}"
          attributes: { value: "{{ inputs.m.missing.deeper }}" }
  - id: say
    type: save_message
    args:
      role: assistant
      content: "cut={{ nodes.plan.cut }}"
    observe:
      metrics:
        - name: said
          kind: counter
          attributes: { x: "{{ inputs.m.missing.deeper }}" }
edges:
  - from: parse
    default: plan
  - from: plan
    default: say
`

func TestObserve_RuntimeIgnoresTheBlock(t *testing.T) {
	sc := func() *scenario.Scenario {
		return &scenario.Scenario{Name: "observed", Events: []scenario.SimulatedEvent{
			{Node: "parse", Output: map[string]interface{}{"stdout": "ok", "exit_code": 0}},
		}}
	}

	observed, err := wfyaml.ParseWorkflow([]byte(observeIgnoredYAML))
	require.NoError(t, err)
	require.NotNil(t, observed.GetObserve())
	plain, err := wfyaml.ParseWorkflow([]byte(observeIgnoredYAML))
	require.NoError(t, err)
	plain.Observe = nil
	for _, n := range plain.GetNodes() {
		require.NotNil(t, n.GetObserve(), "every node of the fixture carries observe")
		n.Observe = nil
	}

	got := NewRunner(observed).Run(sc())
	want := NewRunner(plain).Run(sc())

	require.Equal(t, "completed", got.Execution.Outcome, "%+v", got.Execution.Error)
	assert.Equal(t, want.Execution.Outcome, got.Execution.Outcome)
	assert.Equal(t, want.Execution.NodesCompleted, got.Execution.NodesCompleted)
	assert.Equal(t, want.Execution.NodeOutputs, got.Execution.NodeOutputs)
	assert.Equal(t, []string{"cut=true"}, savedContent(got, "say"))
}
