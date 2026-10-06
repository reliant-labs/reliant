package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// automation_only is the workflow's Chat trigger turned off: the builder's
// Chat card writes it, the launcher refuses a chat start of it, and chat
// pickers leave it out. The builder saves by marshalling the parsed proto,
// so a marshaller that drops it would silently turn chat back on.
func TestAutomationOnly_RoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: nightly-digest
automation_only: true
entry: [a]
triggers:
  - name: nightly
    schedule: { cron: ["0 9 * * 1-5"] }
nodes:
  - id: a
    type: compact
`))
	require.NoError(t, err)
	assert.True(t, wf.GetAutomationOnly())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "automation_only: true\n")

	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.True(t, again.GetAutomationOnly(), "round trip lost automation_only:\n%s", out)
}

// A chat-startable workflow (the default) must not gain an
// `automation_only: false` line on every save.
func TestAutomationOnly_OmittedWhenUnset(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: plain
entry: [a]
nodes:
  - id: a
    type: compact
`))
	require.NoError(t, err)
	assert.False(t, wf.GetAutomationOnly())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "automation_only"), "unset automation_only emitted:\n%s", out)
}

// A declared trigger's prompt template is part of its WHEN, so it is in the
// YAML the agent tools and the builder both write.
func TestTriggerPrompt_RoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: triage
entry: [a]
triggers:
  - name: new-issue
    integration: { integration: github, events: [issues.opened] }
    prompt: "Triage issue #{{ trigger.payload.data.issue.number }}"
nodes:
  - id: a
    type: compact
`))
	require.NoError(t, err)
	require.Len(t, wf.GetTriggers(), 1)
	assert.Equal(t, "Triage issue #{{ trigger.payload.data.issue.number }}", wf.GetTriggers()[0].GetPrompt())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.Equal(t, wf.GetTriggers()[0].GetPrompt(), again.GetTriggers()[0].GetPrompt(), "round trip changed the prompt:\n%s", out)
}
