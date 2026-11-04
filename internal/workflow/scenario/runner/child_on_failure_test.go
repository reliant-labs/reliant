// Copyright (c) 2025 Reliant Labs
package runner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The shape of release-cp → project://release: a wrapper whose only node runs
// a ref'd child, and both declare on_failure.
const childWithHandlerYAML = `
name: child
inputs:
  boom: {type: boolean, default: false}
  verdict: {type: string, default: ok}
  m: {type: object, default: {}}
entry: [probe]
nodes:
  # A step the scenario mocks, which is what opens the ref (runs its body).
  - id: probe
    type: run
    timeout: 1m
    args: {command: check}
  - id: prep
    type: compute
    outputs:
      n: "{{ 7 }}"
  # Args that cannot be evaluated: an error nothing in the child handles.
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
  - id: done
    type: save_message
    args: {role: assistant, content: done}
  - id: failed
    type: save_message
    outcome: failure
    args: {role: assistant, content: verdict failure}
edges:
  - from: probe
    default: prep
  - from: prep
    default: work
  - from: work
    cases:
      - {to: failed, condition: "inputs.verdict == 'fail'"}
    default: done
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "child handler: {{ failure.node_id }} {{ failure.node_path }} {{ failure.error_code }} n={{ nodes.?prep.n.orValue(-1) }} verdict={{ inputs.verdict }}"
`

const wrapperYAML = `
name: wrapper
inputs:
  boom: {type: boolean, default: false}
  verdict: {type: string, default: ok}
entry: [release]
nodes:
  - id: release
    type: workflow
    ref: project://child
    args:
      boom: "{{ inputs.boom }}"
      verdict: "{{ inputs.verdict }}"
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "parent handler: {{ failure.node_path }}"
`

func runWrapper(t *testing.T, inputs map[string]interface{}, overrides map[string]versionOverride) *Result {
	t.Helper()
	wrapper, err := wfyaml.ParseWorkflow([]byte(wrapperYAML))
	require.NoError(t, err)
	child, err := wfyaml.ParseWorkflow([]byte(childWithHandlerYAML))
	require.NoError(t, err)
	r := New(wrapper, Options{Loader: func(ref string) (*reliantv1.Workflow, error) {
		if ref == "project://child" || ref == "child" {
			return child, nil
		}
		return nil, fmt.Errorf("no workflow %q", ref)
	}})
	r.versionOverrides = overrides
	return r.Run(&scenario.Scenario{Name: "run", Inputs: inputs, Events: []scenario.SimulatedEvent{
		{Node: "release.probe", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
}

// savedTexts is every saved message's content, in the order the run saved
// them.
func savedTexts(res *Result) []string {
	var out []string
	for _, m := range res.Execution.SavedMessages {
		out = append(out, m.Content)
	}
	return out
}

func indexContaining(texts []string, needle string) int {
	for i, s := range texts {
		if strings.Contains(s, needle) {
			return i
		}
	}
	return -1
}

// A ref'd child that fails runs ITS handler, in its own scope, before the
// failure reaches the parent, whose handler runs after it. The run still
// fails.
func TestRefChildOnFailure_RunsBeforeTheParentsHandler(t *testing.T) {
	res := runWrapper(t, map[string]interface{}{"boom": true}, nil)
	require.Equal(t, "error", res.Execution.Outcome, "the handlers do not mask the failure: %+v", res.Execution.Error)

	texts := savedTexts(res)
	child := indexContaining(texts, "child handler:")
	parent := indexContaining(texts, "parent handler:")
	require.GreaterOrEqual(t, child, 0, "the child's handler ran: %q", texts)
	require.GreaterOrEqual(t, parent, 0, "the parent's handler still ran: %q", texts)
	assert.Less(t, child, parent, "child first, then parent: %q", texts)

	assert.Contains(t, texts[child], "child handler: work work node_error", "failure is relative to the child")
	assert.Contains(t, texts[child], "n=7", "the handler reads the child's node outputs")
	assert.Contains(t, texts[child], "verdict=ok", "and the child's inputs")
	assert.Contains(t, texts[parent], "parent handler: release.work", "the parent sees the run-wide path")

	// Addressed in the child's scope, never colliding with the root handler.
	assert.Len(t, savedContent(res, "release.on_failure.note"), 1)
	assert.Len(t, savedContent(res, "on_failure.note"), 1)
}

// A child node that declares outcome: failure fails the child for its
// handler, as it would a root run.
func TestRefChildOnFailure_RunsOnADeclaredFailureVerdict(t *testing.T) {
	res := runWrapper(t, map[string]interface{}{"verdict": "fail"}, nil)
	texts := savedTexts(res)
	i := indexContaining(texts, "child handler:")
	require.GreaterOrEqual(t, i, 0, "%q", texts)
	assert.Contains(t, texts[i], "child handler: failed failed outcome")
}

func TestRefChildOnFailure_NotRunWhenTheChildCompletes(t *testing.T) {
	res := runWrapper(t, nil, nil)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, -1, indexContaining(savedTexts(res), "handler:"), "%q", savedTexts(res))
}

// A run recorded before the change has no version marker: replayed (or
// still running) on this code it takes the old path, failing straight through
// to the parent with no new commands.
func TestRefChildOnFailure_VersionGateKeepsOldRunsUnchanged(t *testing.T) {
	res := runWrapper(t, map[string]interface{}{"boom": true}, map[string]versionOverride{
		"ref-child-on-failure": {max: 1, returns: workflow.DefaultVersion},
		// Once any GetVersion is mocked, the test env answers unmocked ids
		// with DefaultVersion; the root handler predates this change.
		"workflow-on-failure": {max: 1, returns: 1},
	})
	require.Equal(t, "error", res.Execution.Outcome)
	texts := savedTexts(res)
	assert.Equal(t, -1, indexContaining(texts, "child handler:"), "%q", texts)
	assert.GreaterOrEqual(t, indexContaining(texts, "parent handler:"), 0, "%q onfail=%+v err=%+v", texts, res.Execution.OnFailure, res.Execution.Error)
}
