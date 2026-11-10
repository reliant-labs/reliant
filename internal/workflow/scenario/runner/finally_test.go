// Copyright (c) 2025 Reliant Labs
package runner

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The shape pr-fix needs: take a lease, do work that may fail or be
// cancelled mid-command, and release the lease however the run ends.
// `work` fails the run when inputs.boom (its args cannot be evaluated);
// `hold` is the long command a cancel interrupts.
const finallyYAML = `
name: leased
inputs:
  m: {type: object, default: {}}
  boom: {type: boolean, default: false}
entry: [lease]
nodes:
  - id: lease
    type: compute
    outputs:
      sha: "{{ 'abc123' }}"
  - id: hold
    type: run
    args:
      command: "sleep 3600"
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
  - id: after
    type: save_message
    args:
      role: assistant
      content: "done"
edges:
  - from: lease
    default: hold
  - from: hold
    default: work
  - from: work
    default: after
on_failure:
  entry: [noted]
  nodes:
    - id: noted
      type: save_message
      args:
        role: assistant
        content: "on_failure at {{ failure.node_id }}"
finally:
  entry: [release]
  nodes:
    - id: release
      type: save_message
      args:
        role: assistant
        content: "released {{ nodes.?lease.sha.orValue('-') }} status={{ run.status }} failed_at={{ failure.node_id }} code={{ failure.error_code }} after={{ has(nodes.after) }}"
`

func runFinally(t *testing.T, inputs map[string]interface{}, cancelAt string, overrides map[string]versionOverride) *Result {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(finallyYAML))
	require.NoError(t, err)
	r := NewRunner(wf)
	r.cancelAtRunStep = cancelAt
	r.versionOverrides = overrides
	return r.Run(&scenario.Scenario{Name: "run", Inputs: inputs, Events: []scenario.SimulatedEvent{
		{Node: "hold", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
}

func TestFinally_RunsOnceWhenTheRunCompletes(t *testing.T) {
	res := runFinally(t, nil, "", nil)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)

	got := savedContent(res, "finally.release")
	require.Len(t, got, 1, "the handler runs exactly once: %q", savedTexts(res))
	assert.Contains(t, got[0], "released abc123 status=completed")
	assert.Contains(t, got[0], "failed_at= code=", "failure is declared, with every key empty, when nothing failed")
	assert.Contains(t, got[0], "after=true", "it runs after the graph")
	assert.Empty(t, savedContent(res, "on_failure.noted"))

	require.NotNil(t, res.Execution.Finally)
	assert.Equal(t, scenario.FinallyObserved{Ran: true, Status: "completed"}, *res.Execution.Finally)
}

// A failure runs on_failure first, then finally, which reads the same failure.
// The run still fails.
func TestFinally_RunsAfterOnFailureWhenTheRunFails(t *testing.T) {
	res := runFinally(t, map[string]interface{}{"boom": true}, "", nil)
	require.Equal(t, "error", res.Execution.Outcome, "the handlers do not mask the failure")

	texts := savedTexts(res)
	onFailure, finally := indexContaining(texts, "on_failure at work"), indexContaining(texts, "released abc123")
	require.GreaterOrEqual(t, onFailure, 0, "%q", texts)
	require.GreaterOrEqual(t, finally, 0, "%q", texts)
	assert.Less(t, onFailure, finally, "on_failure first, then finally: %q", texts)
	assert.Contains(t, texts[finally], "status=failed failed_at=work code=node_error after=false")
	assert.Equal(t, "failed", res.Execution.Finally.Status)
}

// The ending finally exists for: the run is cancelled while a command runs.
// finally runs, reading what the run had produced; on_failure does not (a
// cancel is not a failure, and existing handlers must not start firing on it).
func TestFinally_RunsWhenTheRunIsCancelled(t *testing.T) {
	res := runFinally(t, nil, "hold", nil)
	assert.NotEqual(t, "completed", res.Execution.Outcome, "the run was cancelled, not completed")

	got := savedContent(res, "finally.release")
	require.Len(t, got, 1, "the handler runs exactly once on a cancel: %q finally=%+v", savedTexts(res), res.Execution.Finally)
	assert.Contains(t, got[0], "released abc123 status=cancelled failed_at= code= after=false")
	assert.Empty(t, savedContent(res, "on_failure.noted"), "on_failure never runs for a cancel")
	assert.Empty(t, savedContent(res, "after"), "nothing after the cancelled step ran")
	assert.Equal(t, "cancelled", res.Execution.Finally.Status)
}

// A run recorded before finally existed replays without it.
func TestFinally_VersionGateKeepsOldRunsUnchanged(t *testing.T) {
	res := runFinally(t, nil, "", map[string]versionOverride{
		"workflow-finally": {max: 1, returns: workflow.DefaultVersion},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Empty(t, savedContent(res, "finally.release"))
}

func TestFinally_ExpectationsAreChecked(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(finallyYAML))
	require.NoError(t, err)
	ran := true
	res := NewRunner(wf).Run(&scenario.Scenario{Name: "expect", Events: []scenario.SimulatedEvent{
		{Node: "hold", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
		{Node: "finally.release", Output: map[string]interface{}{}},
	}, Expect: &scenario.Expectation{Finally: &scenario.FinallyExpectation{Ran: &ran, Status: "failed"}}})
	assert.Contains(t, res.Mismatches, `expected run.status "failed" but got "completed"`, "%+v", res.Mismatches)
}

func TestFinallyPathPrefixMatchesRuntime(t *testing.T) {
	assert.Equal(t, "finally", runtime.FinallyNodeID)
}

// A ref'd child that declares finally runs its own when it ends, in its scope
// and before the parent's.
func TestRefChildFinally_RunsBeforeTheParentsFinally(t *testing.T) {
	const child = `
name: child
entry: [probe]
nodes:
  - id: probe
    type: run
    args:
      command: "true"
finally:
  entry: [release]
  nodes:
    - id: release
      type: save_message
      args:
        role: assistant
        content: "child finally: {{ run.status }} probe={{ has(nodes.probe) }}"
`
	const wrapper = `
name: wrapper
entry: [release]
nodes:
  - id: release
    type: workflow
    ref: project://child
finally:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "parent finally: {{ run.status }}"
`
	w, err := wfyaml.ParseWorkflow([]byte(wrapper))
	require.NoError(t, err)
	c, err := wfyaml.ParseWorkflow([]byte(child))
	require.NoError(t, err)
	r := New(w, Options{Loader: func(ref string) (*reliantv1.Workflow, error) {
		if ref == "project://child" || ref == "child" {
			return c, nil
		}
		return nil, fmt.Errorf("no workflow %q", ref)
	}})
	res := r.Run(&scenario.Scenario{Name: "run", Events: []scenario.SimulatedEvent{
		{Node: "release.probe", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)

	texts := savedTexts(res)
	childAt, parentAt := indexContaining(texts, "child finally: completed probe=true"), indexContaining(texts, "parent finally: completed")
	require.GreaterOrEqual(t, childAt, 0, "%q", texts)
	require.GreaterOrEqual(t, parentAt, 0, "%q", texts)
	assert.Less(t, childAt, parentAt, "%q", texts)
	assert.Len(t, savedContent(res, "release.finally.release"), 1)
	assert.Len(t, savedContent(res, "finally.note"), 1)
}
