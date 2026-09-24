// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These pin the scenario-runner capabilities the builtin real-runtime lane
// depends on: saved messages are observed (delegated saves resolved against
// the mocked result through the runtime's own resolver), a resolution error
// fails the run as it does in production, start_at is the runtime's real
// resume mode, tool-call inputs reach the workflow in wire shape, and a
// router-only graph can route.

func runInline(t *testing.T, workflowYAML, scenarioYAML string) *Result {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)
	scenarios, err := scenario.ParseScenarioYAML([]byte(scenarioYAML))
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	return NewRunner(wf).Run(scenarios[0])
}

// An activity-backed node's save_message is delegated to the activity in
// production. The runner resolves it on the mock through
// runtime.ResolveDelegatedSaveMessage, so its condition and templates run.
const delegatedSaveWorkflow = `
name: delegated-save
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args: {model: {tags: [fast]}}
    save_message:
      condition: "output.response_text != ''"
      role: assistant
      content: "Answer: {{output.response_text}}"
`

func TestRunner_ObservesDelegatedSave(t *testing.T) {
	res := runInline(t, delegatedSaveWorkflow, `
name: saves
events:
  - node: ask
    type: llm_response
    text: "forty-two"
expect:
  outcome: completed
  messages:
    ask:
      saved: true
      count: 1
      role: assistant
      content_contains: ["Answer: forty-two"]
`)
	assert.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
	require.Len(t, res.Execution.SavedMessages, 1)
}

func TestRunner_DelegatedSaveConditionFalseSavesNothing(t *testing.T) {
	res := runInline(t, delegatedSaveWorkflow, `
name: no-save
events:
  - node: ask
    type: llm_response
    text: ""
expect:
  outcome: completed
  messages:
    ask: {saved: false}
`)
	assert.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
}

// A save_message template that cannot resolve fails the run — the production
// behaviour (ActivityWrapper returns a non-retryable error). Before the runner
// resolved delegated saves, this passed silently.
func TestRunner_DelegatedSaveResolutionErrorFailsTheRun(t *testing.T) {
	res := runInline(t, `
name: broken-save
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args: {model: {tags: [fast]}}
    save_message:
      role: assistant
      content: "{{output.response_data.missing.field}}"
`, `
name: broken
events:
  - node: ask
    type: llm_response
    text: "hi"
expect:
  outcome: error
`)
	// The activity fails non-retryably; the runtime treats that as retry
	// exhaustion and pauses the chat, which the test environment surfaces as
	// a deadline. The point is that the run FAILS instead of passing.
	assert.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
	require.NotNil(t, res.Execution.Error)
}

// start_at enters through the runtime's real resume mode: earlier nodes do
// not run, and their outputs are NOT reconstructed — so an unguarded read of
// one fails exactly as it would on a real resumed run.
const resumeWorkflow = `
name: resume
entry: [first]
nodes:
  - {id: first, type: call_llm, args: {model: {tags: [fast]}}}
  - id: second
    type: save_message
    args: {role: assistant, content: "%s"}
edges: [{from: first, to: second}]
`

func TestRunner_StartAtIsRealResume(t *testing.T) {
	res := runInline(t, strings.Replace(resumeWorkflow, "%s",
		"{{has(nodes.first) ? nodes.first.response_text : 'resumed'}}", 1), `
name: resumed
start_at: second
events:
  - node: second
    output: {}
expect:
  outcome: completed
  not_reached: [first]
  messages:
    second: {content_contains: ["resumed"]}
`)
	assert.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)

	res = runInline(t, strings.Replace(resumeWorkflow, "%s", "{{nodes.first.response_text}}", 1), `
name: resumed-unguarded
start_at: second
events: []
expect:
  outcome: error
`)
	assert.Equal(t, scenario.StatusPassed, res.Status,
		"an unguarded read of a node before the resume point must fail: %v", res.Mismatches)
}

// A graph whose only LLM work is a router must still register CallLLM (the
// routing decision is a CallLLM step).
func TestRunner_RouterOnlyGraphRoutes(t *testing.T) {
	res := runInline(t, `
name: router-only
entry: [classify]
nodes:
  - id: classify
    type: router
    model: {tags: [fast]}
    nodes:
      - {id: a, description: first}
      - {id: b, description: second}
  - {id: a, type: save_message, args: {role: assistant, content: "took a"}}
  - {id: b, type: save_message, args: {role: assistant, content: "took b"}}
`, `
name: routes
events:
  - node: classify
    output: {selected_node: b, reasoning: "b"}
  - node: b
    output: {}
expect:
  outcome: completed
  reached: [classify, b]
  not_reached: [a]
  messages:
    b: {content_contains: ["took b"]}
`)
	assert.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
}
func TestEncodeToolCallInputs(t *testing.T) {
	raw := map[string]interface{}{
		"tool_calls": []interface{}{
			map[string]interface{}{"id": "a", "input": map[string]interface{}{"x": 1}},
			map[string]interface{}{"id": "b", "input": `{"y":2}`},
		},
	}
	out := encodeToolCallInputs(raw)
	calls := out["tool_calls"].([]interface{})
	assert.Equal(t, `{"x":1}`, calls[0].(map[string]interface{})["input"])
	assert.Equal(t, `{"y":2}`, calls[1].(map[string]interface{})["input"])
	// The mock itself is not mutated.
	_, stillMap := raw["tool_calls"].([]interface{})[0].(map[string]interface{})["input"].(map[string]interface{})
	assert.True(t, stillMap)
}
