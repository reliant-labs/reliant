// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A compute node turns CEL into named outputs inside the workflow: no model, no
// shell, no activity. The scenario below needs NO mock event for it.
const computeYAML = `
name: compute-basic
inputs:
  adds: {type: integer, default: 40}
  dels: {type: integer, default: 30}
  labels: {type: array, default: ["agent:ready", "risk:low"]}
entry: [parse]
nodes:
  - id: parse
    type: run
    args:
      command: "echo ok"
  - id: facts
    type: compute
    outputs:
      size: "{{ inputs.adds + inputs.dels < 60 ? 'size:S' : (inputs.adds + inputs.dels < 400 ? 'size:M' : 'size:L') }}"
      ready: "{{ 'agent:ready' in inputs.labels }}"
      total: "{{ inputs.adds + inputs.dels }}"
      lease_until: "{{ string(timestamp('2026-10-14T03:00:00Z') + duration('45m')) }}"
      status: "{{ json.decode(nodes.parse.stdout).?status.orValue('error') }}"
      note: "{{ nodes.parse.exit_code }} exit, {{ inputs.adds }} added"
      fixed: 7
      tags: ["a", "{{ inputs.labels[0] }}"]
  - id: use
    type: save_message
    args:
      role: assistant
      content: "{{ nodes.facts.size }} ready={{ nodes.facts.ready }} total={{ nodes.facts.total }}"
edges:
  - from: parse
    default: facts
  - from: facts
    default: use
`

func TestComputeNode_OutputsAreTypedAndNeedNoMock(t *testing.T) {
	res := runYAMLScenario(t, computeYAML, &scenario.Scenario{
		Name:   "compute",
		Events: []scenario.SimulatedEvent{{Node: "parse", Output: map[string]interface{}{"stdout": `{"status":"ready"}`, "exit_code": 0}}},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Contains(t, res.Execution.NodesCompleted, "facts", "a compute node completes")
	assert.Empty(t, res.Warnings, "no unmocked-activity warning: compute dispatches no activity")

	out := res.Execution.NodeOutputs["facts"]
	assert.Equal(t, "size:M", out["size"])
	assert.Equal(t, true, out["ready"], "a pure {{ }} keeps its type")
	assert.EqualValues(t, 70, out["total"])
	assert.Equal(t, "2026-10-14T03:45:00Z", out["lease_until"])
	assert.Equal(t, "ready", out["status"])
	assert.Equal(t, "0 exit, 40 added", out["note"], "text around a hole makes a string")
	assert.EqualValues(t, 7, out["fixed"], "a non-string value is a literal")
	assert.Equal(t, []interface{}{"a", "agent:ready"}, out["tags"], "templates resolve inside lists")

	assert.Equal(t, []string{"size:M ready=true total=70"}, savedContent(res, "use"))
}

func TestComputeNode_FailingExpressionFailsTheStep(t *testing.T) {
	res := runYAMLScenario(t, `
name: compute-fails
inputs:
  m: {type: object, default: {}}
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      ok: "{{ 1 + 1 }}"
      bad: "{{ inputs.m.missing.deeper }}"
`, &scenario.Scenario{Name: "fails"})
	require.Equal(t, "error", res.Execution.Outcome, "a compute error is the run's error, never a quiet null")
	require.NotNil(t, res.Execution.Error)
	assert.Equal(t, "c", res.Execution.Error.Node)
	assert.Contains(t, res.Execution.Error.Message, "bad")
}

func TestComputeNode_ConditionSkipsIt(t *testing.T) {
	res := runYAMLScenario(t, `
name: compute-skip
inputs:
  go: {type: boolean, default: false}
entry: [c]
nodes:
  - id: c
    type: compute
    condition: "inputs.go"
    outputs:
      x: "{{ 1 }}"
`, &scenario.Scenario{Name: "skip", Expect: &scenario.Expectation{Outcome: "completed", Skipped: []string{"c"}}})
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
}

// Inside a loop body each iteration computes from its own item.
func TestComputeNode_InsideALoopSeesTheIteration(t *testing.T) {
	res := runYAMLScenario(t, `
name: compute-loop
entry: [each]
nodes:
  - id: each
    type: loop
    while: "iter.iteration < 3"
    items: "{{ [{'n': 2}, {'n': 5}] }}"
    inline:
      entry: [sq]
      outputs:
        squared: "{{ nodes.sq.squared }}"
      nodes:
        - id: sq
          type: compute
          outputs:
            squared: "{{ iter.item.n * iter.item.n }}"
`, &scenario.Scenario{Name: "loop"})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.EqualValues(t, 25, res.Execution.NodeOutputs["each"]["squared"], "the last iteration's output")
}

func TestComputeNode_ExpectNodeOutputs(t *testing.T) {
	res := runYAMLScenario(t, computeYAML, &scenario.Scenario{
		Name:   "expect",
		Events: []scenario.SimulatedEvent{{Node: "parse", Output: map[string]interface{}{"stdout": "not json", "exit_code": 0}}},
		Expect: &scenario.Expectation{
			Outcome:     "completed",
			Completed:   []string{"facts"},
			NodeOutputs: map[string]map[string]interface{}{"facts": {"size": "size:M", "status": "error"}},
		},
	})
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
}
