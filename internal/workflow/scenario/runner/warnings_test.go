// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scenario that fires a trigger starts the run with the event itself as
// `trigger`, the way a fired run does, so a node reading trigger.payload is
// testable (it used to fail "no such key: payload").
func TestScenarioTriggerPayloadReachesTheNodes(t *testing.T) {
	const wf = `
name: hooked
triggers:
- name: hook
  webhook: {}
entry: [say]
nodes:
- id: say
  type: save_message
  args:
    role: assistant
    content: 'status={{ trigger.payload.body.status }} kind={{ trigger.kind }}'
`
	res := runYAMLScenario(t, wf, &scenario.Scenario{
		Name: "firing",
		Trigger: &scenario.TriggerFiring{Name: "hook", Payload: map[string]any{
			"body": map[string]any{"status": "firing"},
		}},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, []string{"status=firing kind=webhook"}, savedContent(res, "say"))
}

// A mock key the node's output type has no field for is an empty mock: a
// run node mocked `exitcode: 1` exits 0 and takes the green path. The run
// still passes (outputs are flexible), but says so.
func TestScenarioWarnsOnAMockKeyTheOutputHasNot(t *testing.T) {
	const wf = `
name: gated
entry: [check]
nodes:
- id: check
  type: run
  args: {command: 'make test'}
- id: ok
  type: save_message
  args: {role: assistant, content: ok}
- id: bad
  type: save_message
  args: {role: assistant, content: bad}
edges:
- from: check
  cases:
  - to: bad
    condition: nodes.check.exit_code != 0
  default: ok
`
	res := runYAMLScenario(t, wf, &scenario.Scenario{
		Name:   "typo",
		Events: []scenario.SimulatedEvent{{Node: "check", Output: map[string]any{"exitcode": 1}}},
	})
	require.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
	assert.True(t, anyContains(res.Warnings, `output field "exitcode"`), "%v", res.Warnings)
}

// A workflow-ref node nothing mocks returns no outputs, so the first symptom
// would be a later CEL "no such key"; the run names the node instead.
func TestScenarioWarnsOnAnUnmockedRefNode(t *testing.T) {
	const wf = `
name: delegating
entry: [diagnose]
nodes:
- id: diagnose
  type: workflow
  args:
    ref: builtin://agent
    inputs: {prompt: why}
- id: done
  type: save_message
  args: {role: assistant, content: done}
edges:
- from: diagnose
  to: done
`
	res := runYAMLScenario(t, wf, &scenario.Scenario{Name: "unmocked"})
	assert.True(t, anyContains(res.Warnings, `node "diagnose" (builtin://agent) was not mocked`), "%v", res.Warnings)

	mocked := runYAMLScenario(t, wf, &scenario.Scenario{
		Name:   "mocked",
		Events: []scenario.SimulatedEvent{{Node: "diagnose", Output: map[string]any{"response_text": "found it"}}},
	})
	assert.False(t, anyContains(mocked.Warnings, "was not mocked"), "%v", mocked.Warnings)
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// An integration firing's payload is merged over a sample that fills every
// field. A field the workflow reads that only the sample set (a Slack
// app_mention's optional data.thread_ts, which the sample fills with
// "thread_ts") is named, so a top-level-mention scenario does not quietly
// test the in-thread branch.
func TestScenarioWarnsWhenTheSampleFillsAFieldTheWorkflowReads(t *testing.T) {
	const wf = `
name: slackbot
triggers:
- name: mention
  integration:
    integration: slack
    events: [app_mention]
entry: [reply]
nodes:
- id: reply
  type: save_message
  args:
    role: assistant
    content: '{{ trigger.payload.data.thread_ts != "" ? "in thread" : "top level" }}'
`
	fire := func(data map[string]any) *Result {
		return runYAMLScenario(t, wf, &scenario.Scenario{
			Name:    "mention",
			Trigger: &scenario.TriggerFiring{Name: "mention", Payload: map[string]any{"data": data}},
		})
	}
	res := fire(map[string]any{"text": "hi"})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, []string{"in thread"}, savedContent(res, "reply"), "the sample's thread_ts still applies: existing scenarios keep their behaviour")
	assert.True(t, anyContains(res.Warnings, `trigger.payload.data.thread_ts`), "%v", res.Warnings)
	assert.False(t, anyContains(res.Warnings, `trigger.payload.data.user`), "a field the workflow never reads is not named: %v", res.Warnings)

	set := fire(map[string]any{"text": "hi", "thread_ts": ""})
	assert.Equal(t, []string{"top level"}, savedContent(set, "reply"))
	assert.False(t, anyContains(set.Warnings, "thread_ts"), "%v", set.Warnings)
}
