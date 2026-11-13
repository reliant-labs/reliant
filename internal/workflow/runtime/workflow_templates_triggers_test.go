// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A workflow that declares triggers must still load for a run that no
// trigger started: a chat, a builder test, a spawn. Its triggers' inputs are
// templates over the launch event, which such a run does not have, so
// resolving them at load would fail every one of those runs.
func TestWorkflowWithTriggersLoadsWithoutALaunchEvent(t *testing.T) {
	yamlData := []byte(`name: triage
inputs:
  issue_number:
    type: integer
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
entry: [a]
nodes:
  - id: a
    type: call_llm
`)
	wf, err := ResolveAndParseWorkflow(yamlData, map[string]interface{}{"issue_number": 7}, nil)
	require.NoError(t, err)
	require.Len(t, wf.GetTriggers(), 1)
	assert.Equal(t, "{{ trigger.payload.data.issue.number }}", wf.GetTriggers()[0].GetInputs()["issue_number"],
		"the template is kept verbatim for the fire path to evaluate")
}

// concurrency.key is a template over `trigger` alone, rendered by the trigger
// layer for each firing. A hand-started run (start_run with inputs only) has
// no trigger payload, so rendering the key at load failed it before any node:
// "key 'concurrency': key 'key': CEL evaluation error: no such key: attributes"
// (control-plane pr-review run 0a8c3463, 2026-11-12).
func TestWorkflowConcurrencyKeyLoadsWithoutALaunchEvent(t *testing.T) {
	yamlData := []byte(`name: review
inputs:
  pr_number:
    type: integer
concurrency:
  max_running: 1
  waiting: latest
  key: "{{ trigger.payload.attributes.repository + '#' + string(trigger.payload.attributes.pr_number) }}"
entry: [a]
nodes:
  - id: a
    type: call_llm
`)
	wf, err := ResolveAndParseWorkflow(yamlData, map[string]interface{}{"pr_number": 7}, nil)
	require.NoError(t, err)
	assert.Equal(t, "{{ trigger.payload.attributes.repository + '#' + string(trigger.payload.attributes.pr_number) }}",
		wf.GetConcurrency().GetKey(), "the key is kept verbatim for the trigger layer to render")
	assert.EqualValues(t, 1, wf.GetConcurrency().GetMaxRunning())
}
