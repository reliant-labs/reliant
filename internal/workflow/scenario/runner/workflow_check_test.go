// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// A CI-shaped workflow with a check: `gate` is the command a cancel
// interrupts, `work` fails the run when inputs.boom (its args cannot be
// evaluated), and `red` routes the run to a failure outcome when inputs.red.
// The check's output reads the gate's result and how the run ended.
const checkYAML = `
name: checked
inputs:
  m: {type: object, default: {}}
  boom: {type: boolean, default: false}
  red: {type: boolean, default: false}
check:
  name: reliant/ci
  output: "{{ {'title': 'gate ' + run.status, 'summary': 'exit ' + string(nodes.?gate.exit_code.orValue(-1))} }}"
entry: [gate]
nodes:
  - id: gate
    type: run
    args:
      command: "task ci"
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
  - id: red
    type: save_message
    condition: "inputs.red"
    outcome: failure
    args:
      role: assistant
      content: "red"
edges:
  - from: gate
    default: work
  - from: work
    default: red
`

// runChecked runs checkYAML (or src) as the launch run of a pull_request
// event, or as a plain run when launched is false, and returns what it
// reported to its check.
func runChecked(t *testing.T, src string, inputs map[string]interface{}, cancelAt string, launched bool) (*Result, *checkReports) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	r := NewRunner(wf)
	r.cancelAtRunStep = cancelAt
	r.checkReports = &checkReports{}
	if launched {
		r.launchedBy = &runtime.TriggerInfo{Kind: "integration", TriggerID: "t1", EventID: "e1",
			Payload: map[string]any{"attributes": map[string]any{"repository": "reliant-labs/reliant"}}}
	}
	res := r.Run(&scenario.Scenario{Name: "run", Inputs: inputs, Events: []scenario.SimulatedEvent{
		{Node: "gate", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	return res, r.checkReports
}

func onlyEnding(t *testing.T, reports *checkReports) core.WorkflowCheckEnding {
	t.Helper()
	require.Len(t, reports.started, 1, "the start is reported once")
	assert.Equal(t, "scenario-chat", reports.started[0].ChatID)
	require.Len(t, reports.ended, 1, "the end is reported once")
	assert.Equal(t, "scenario-chat", reports.ended[0].ChatID)
	return reports.ended[0].Ending
}

func TestCheck_ACompletedRunReportsSuccessAndItsOutput(t *testing.T) {
	res, reports := runChecked(t, checkYAML, nil, "", true)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	ending := onlyEnding(t, reports)
	assert.Equal(t, runtime.RunStatusCompleted, ending.Status)
	assert.Empty(t, ending.Verdict)
	assert.Empty(t, ending.OutputError)
	assert.Equal(t, map[string]any{"title": "gate completed", "summary": "exit 0"}, ending.Output)
}

func TestCheck_AFailureOutcomeReportsFailed(t *testing.T) {
	_, reports := runChecked(t, checkYAML, map[string]interface{}{"red": true}, "", true)
	ending := onlyEnding(t, reports)
	assert.Equal(t, runtime.RunStatusFailed, ending.Status)
	assert.Equal(t, "failure", ending.Verdict)
	assert.Equal(t, "gate failed", ending.Output["title"])
}

func TestCheck_AFailedRunReportsFailed(t *testing.T) {
	res, reports := runChecked(t, checkYAML, map[string]interface{}{"boom": true}, "", true)
	require.Equal(t, "error", res.Execution.Outcome)
	ending := onlyEnding(t, reports)
	assert.Equal(t, runtime.RunStatusFailed, ending.Status)
	assert.Equal(t, "gate failed", ending.Output["title"])
}

func TestCheck_ACancelledRunReportsCancelled(t *testing.T) {
	_, reports := runChecked(t, checkYAML, nil, "gate", true)
	ending := onlyEnding(t, reports)
	assert.Equal(t, runtime.RunStatusCancelled, ending.Status)
	assert.Equal(t, "gate cancelled", ending.Output["title"])
	assert.Equal(t, "exit -1", ending.Output["summary"], "the gate never finished")
}

func TestCheck_AnOutputThatFailsIsReportedNotFatal(t *testing.T) {
	src := strings.Replace(checkYAML, "output: \"{{ {'title': 'gate ' + run.status, 'summary': 'exit ' + string(nodes.?gate.exit_code.orValue(-1))} }}\"",
		"output: \"{{ nodes.gate.no_such_field }}\"", 1)
	res, reports := runChecked(t, src, nil, "", true)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	ending := onlyEnding(t, reports)
	assert.Equal(t, runtime.RunStatusCompleted, ending.Status)
	assert.Nil(t, ending.Output)
	assert.Contains(t, ending.OutputError, "check.output")
}

// Only the run a trigger launched reports: a chat or builder start of the
// same workflow has no check, and a workflow without check: reports nothing.
func TestCheck_OnlyATriggersLaunchRunReports(t *testing.T) {
	_, reports := runChecked(t, checkYAML, nil, "", false)
	assert.Empty(t, reports.started)
	assert.Empty(t, reports.ended)

	plain := strings.Replace(checkYAML, "check:\n  name: reliant/ci\n", "x_check:\n  name: reliant/ci\n", 1)
	plain = strings.Replace(plain, "x_check:\n  name: reliant/ci\n  output: \"{{ {'title': 'gate ' + run.status, 'summary': 'exit ' + string(nodes.?gate.exit_code.orValue(-1))} }}\"\n", "", 1)
	require.NotContains(t, plain, "check:")
	_, reports = runChecked(t, plain, nil, "", true)
	assert.Empty(t, reports.started)
	assert.Empty(t, reports.ended)
}

// The activity names the run calls are the ones the worker registers.
func TestCheck_ActivityNames(t *testing.T) {
	assert.Equal(t, "WorkflowCheckStarted", workflowcheck.StartedActivityName)
	assert.Equal(t, "WorkflowCheckEnded", workflowcheck.EndedActivityName)
}
