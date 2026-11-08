// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Supplied state must be visible to real CEL evaluation, not just assertions
// over mocked activity outputs. It must not execute the seeded node.
func TestRunner_StateSeedsNodeOutputs(t *testing.T) {
	for _, start := range []string{"", "start_at: second"} {
		t.Run(start, func(t *testing.T) {
			res := runInline(t, `
name: seeded-state
entry: [second]
outputs:
  answer: "{{ nodes.second.answer }}"
nodes:
  - id: first
    type: compute
    outputs: {value: "must not execute"}
  - id: second
    type: compute
    outputs:
      answer: "{{ nodes.first.value }}"
`, `
name: seeded
`+start+`
state:
  first: {value: supplied}
events: []
expect:
  outcome: completed
  reached: [second]
  not_reached: [first]
  outputs: {answer: supplied}
`)
			assert.Equal(t, scenario.StatusPassed, res.Status, "error: %+v; mismatches: %v", res.Execution.Error, res.Mismatches)
			assert.NotContains(t, res.Execution.NodesCompleted, "first", "a fixture is not an executed node")
		})
	}
}

// Reproduce the original boolean fixture and edge condition with a genuinely
// different entry point: start_at must skip fetch_context rather than run it.
func TestRunner_StateWithStartAtControlsRealRouting(t *testing.T) {
	for _, approved := range []string{"true", "false"} {
		t.Run(approved, func(t *testing.T) {
			want, other := "accepted", "rejected"
			if approved == "false" {
				want, other = other, want
			}
			res := runInline(t, `
name: seeded-routing
entry: [fetch_context]
outputs:
  answer: "{{ nodes.finish.answer }}"
nodes:
  - id: fetch_context
    type: compute
    outputs: {approved: false}
  - id: decide
    type: compute
    outputs: {ready: true}
  - id: accepted
    type: compute
    outputs: {answer: accepted}
  - id: rejected
    type: compute
    outputs: {answer: rejected}
  - id: finish
    type: compute
    outputs:
      answer: "{{ has(nodes.accepted) ? nodes.accepted.answer : nodes.rejected.answer }}"
edges:
  - {from: fetch_context, to: decide}
  - from: decide
    cases:
      - to: accepted
        condition: "nodes.fetch_context.approved"
    default: rejected
  - {from: accepted, to: finish}
  - {from: rejected, to: finish}
`, `
name: seeded-routing
start_at: decide
state:
  fetch_context: {approved: `+approved+`}
events: []
expect:
  outcome: completed
  reached: [decide, `+want+`, finish]
  not_reached: [fetch_context, `+other+`]
  outputs: {answer: `+want+`}
`)
			assert.Equal(t, scenario.StatusPassed, res.Status, "error: %+v; mismatches: %v", res.Execution.Error, res.Mismatches)
		})
	}
}

func TestRunner_StateDoesNotLeakIntoInlineWorkflow(t *testing.T) {
	res := runInline(t, `
name: seeded-root
entry: [phase]
outputs:
  root: "{{ nodes.first.value }}"
  child: "{{ nodes.phase.value }}"
nodes:
  - id: first
    type: compute
    outputs: {value: unused}
  - id: phase
    type: workflow
    inline:
      entry: [read]
      nodes:
        - id: first
          type: compute
          outputs: {value: unused}
        - id: read
          type: compute
          outputs:
            value: "{{ has(nodes.first) ? nodes.first.value : 'isolated' }}"
      outputs:
        value: "{{ nodes.read.value }}"
`, `
name: isolated
state:
  first: {value: root-fixture}
events: []
expect:
  outcome: completed
  reached: [phase, phase.read]
  not_reached: [first, phase.first]
  outputs: {root: root-fixture, child: isolated}
`)
	assert.Equal(t, scenario.StatusPassed, res.Status, "error: %+v; mismatches: %v", res.Execution.Error, res.Mismatches)
}

func TestRunner_NoStateDoesNotReconstructOutputs(t *testing.T) {
	for _, start := range []string{"", "start_at: read"} {
		t.Run(start, func(t *testing.T) {
			res := runInline(t, `
name: no-fixtures
entry: [read]
nodes:
  - id: first
    type: compute
    outputs: {value: unused}
  - id: read
    type: compute
    outputs:
      value: "{{ nodes.first.value }}"
`, `
name: unseeded
`+start+`
events: []
expect:
  outcome: error
  not_reached: [first]
`)
			require.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
			require.NotNil(t, res.Execution.Error)
			assert.Contains(t, res.Execution.Error.Message, "no such key: first")
		})
	}
}
