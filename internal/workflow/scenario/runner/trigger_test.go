// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const triggeredYAML = `
name: triggered
inputs:
  pr_number: {type: integer, default: 0}
  urgent: {type: boolean, default: false}
triggers:
- name: queued
  integration:
    integration: github
    events: [pull_request.labeled]
    match: {repository: acme/app, label: 'merge:queue'}
  filter: "trigger.payload.data.label == 'merge:queue'"
  inputs:
    pr_number: '{{ trigger.payload.data.pull_request.number }}'
entry: [say]
nodes:
- id: say
  type: save_message
  args:
    role: assistant
    content: 'queued #{{ inputs.pr_number }} urgent={{ inputs.urgent }}'
`

// A scenario that fires a trigger starts the run with what the trigger's
// inputs mapping yields, so the mapping is tested with the graph.
func TestScenarioTriggerMapsTheRunsInputs(t *testing.T) {
	res := runYAMLScenario(t, triggeredYAML, &scenario.Scenario{
		Name: "queued",
		Trigger: &scenario.TriggerFiring{Name: "queued", Payload: map[string]any{
			"data": map[string]any{"label": "merge:queue", "pull_request": map[string]any{"number": 42}},
		}},
		Inputs: map[string]any{"urgent": true},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, []string{"queued #42 urgent=true"}, savedContent(res, "say"), "the mapping sets pr_number; inputs: apply over it")
}

// An event the trigger's filter rejects starts no run, so the scenario
// cannot pass as if it had.
func TestScenarioTriggerFilterRejectIsAnError(t *testing.T) {
	res := runYAMLScenario(t, triggeredYAML, &scenario.Scenario{
		Name: "wrong label",
		Trigger: &scenario.TriggerFiring{Name: "queued", Payload: map[string]any{
			"data": map[string]any{"label": "review:passed", "pull_request": map[string]any{"number": 42}},
		}},
	})
	require.Equal(t, scenario.StatusError, res.Status)
	require.NotNil(t, res.Execution.Error)
	assert.Contains(t, res.Execution.Error.Message, "filter rejects this payload")
}

func TestScenarioTriggerMustBeDeclared(t *testing.T) {
	res := runYAMLScenario(t, triggeredYAML, &scenario.Scenario{
		Name:    "typo",
		Trigger: &scenario.TriggerFiring{Name: "queud"},
	})
	require.Equal(t, scenario.StatusError, res.Status)
	assert.Contains(t, res.Mismatches[0], `declares no trigger "queud"`)
}
