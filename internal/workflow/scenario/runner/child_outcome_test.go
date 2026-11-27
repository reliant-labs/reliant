// Copyright (c) 2025 Reliant Labs
package runner

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A child with no on_failure or finally: nothing in it arms a failure scope,
// and its declared failure must still reach the parent's record.
const childWithoutHandlerYAML = `
name: plain
inputs:
  verdict: {type: string, default: ok}
entry: [probe]
nodes:
  - id: probe
    type: run
    timeout: 1m
    args: {command: check}
  - id: done
    type: save_message
    args: {role: assistant, content: done}
  - id: blocked
    type: save_message
    outcome: failure
    args: {role: assistant, content: blocked}
edges:
  - from: probe
    cases:
      - {to: blocked, condition: "inputs.verdict == 'fail'"}
    default: done
`

// wrapperFor is the release-cp shape over child ref, with the `workflow:`
// node's extra keys (propagate_outcome, tolerate_failure) spliced in.
func wrapperFor(ref, nodeExtra string) string {
	return fmt.Sprintf(`
name: wrapper
inputs:
  verdict: {type: string, default: ok}
entry: [release]
nodes:
  - id: release
    type: workflow
    ref: %s
%s    args:
      verdict: "{{ inputs.verdict }}"
  - id: after
    type: save_message
    args: {role: assistant, content: "after release"}
edges:
  - from: release
    default: after
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "parent handler: {{ failure.node_path }} {{ failure.error_code }}"
`, ref, nodeExtra)
}

type outcomeRun struct {
	res      *Result
	recorded []map[string]interface{}
}

func runChildOutcome(t *testing.T, wrapperYAML string, verdict string, overrides map[string]versionOverride) outcomeRun {
	t.Helper()
	wrapper, err := wfyaml.ParseWorkflow([]byte(wrapperYAML))
	require.NoError(t, err)
	withHandler, err := wfyaml.ParseWorkflow([]byte(childWithHandlerYAML))
	require.NoError(t, err)
	plain, err := wfyaml.ParseWorkflow([]byte(childWithoutHandlerYAML))
	require.NoError(t, err)
	r := New(wrapper, Options{Loader: func(ref string) (*reliantv1.Workflow, error) {
		switch ref {
		case "project://child", "child":
			return withHandler, nil
		case "project://plain", "plain":
			return plain, nil
		}
		return nil, fmt.Errorf("no workflow %q", ref)
	}})
	r.versionOverrides = overrides
	var mu sync.Mutex
	var recorded []map[string]interface{}
	r.onRecordedFailure = func(in map[string]interface{}) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, in)
	}
	res := r.Run(&scenario.Scenario{Name: "run", Inputs: map[string]interface{}{"verdict": verdict}, Events: []scenario.SimulatedEvent{
		{Node: "release.probe", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	mu.Lock()
	defer mu.Unlock()
	return outcomeRun{res: res, recorded: recorded}
}

// By default a child's declared failure does not fail the parent, which goes
// on to its next node, but the declaring node is put on record as a failed
// attempt: the run is judged failure_handled, not succeeded.
func TestChildDeclaredFailure_RecordedAndTheParentGoesOn(t *testing.T) {
	for _, ref := range []string{"project://child", "project://plain"} {
		t.Run(ref, func(t *testing.T) {
			run := runChildOutcome(t, wrapperFor(ref, ""), "fail", nil)
			require.Equal(t, "completed", run.res.Execution.Outcome, "%+v", run.res.Execution.Error)
			assert.GreaterOrEqual(t, indexContaining(savedTexts(run.res), "after release"), 0, "the parent went on: %q", savedTexts(run.res))
			assert.Equal(t, -1, indexContaining(savedTexts(run.res), "parent handler:"), "the parent did not fail: %q", savedTexts(run.res))

			require.Len(t, run.recorded, 1, "the declaring node is recorded once: %+v", run.recorded)
			in := run.recorded[0]
			declaring := map[string]string{"project://child": "failed", "project://plain": "blocked"}[ref]
			assert.Equal(t, declaring, in["step_id"])
			assert.Equal(t, "release."+declaring, in["node_path"], "the run-wide path names it")
			assert.Equal(t, "declared the sub-workflow's outcome: failure", in["error"])
		})
	}
}

// The child's own handler still runs first, as before.
func TestChildDeclaredFailure_ChildHandlerStillRuns(t *testing.T) {
	run := runChildOutcome(t, wrapperFor("project://child", ""), "fail", nil)
	assert.GreaterOrEqual(t, indexContaining(savedTexts(run.res), "child handler: failed failed outcome"), 0, "%q", savedTexts(run.res))
}

func TestChildDeclaredFailure_NothingRecordedWhenTheChildSucceeds(t *testing.T) {
	run := runChildOutcome(t, wrapperFor("project://plain", ""), "ok", nil)
	require.Equal(t, "completed", run.res.Execution.Outcome, "%+v", run.res.Execution.Error)
	assert.Empty(t, run.recorded)
}

// A run recorded before the change replays without the record's activity.
func TestChildDeclaredFailure_VersionGateKeepsOldRunsUnchanged(t *testing.T) {
	run := runChildOutcome(t, wrapperFor("project://plain", ""), "fail", map[string]versionOverride{
		"child-declared-failure-record": {max: 1, returns: workflow.DefaultVersion},
	})
	require.Equal(t, "completed", run.res.Execution.Outcome, "%+v", run.res.Execution.Error)
	assert.Empty(t, run.recorded)
}

// propagate_outcome: the child's declared failure fails the parent's node,
// so the parent's handler runs, naming the declaring node, and the run fails.
func TestChildDeclaredFailure_PropagateOutcomeFailsTheParent(t *testing.T) {
	for _, ref := range []string{"project://child", "project://plain"} {
		t.Run(ref, func(t *testing.T) {
			run := runChildOutcome(t, wrapperFor(ref, "    propagate_outcome: true\n"), "fail", nil)
			require.Equal(t, "error", run.res.Execution.Outcome, "the parent fails")
			texts := savedTexts(run.res)
			declaring := map[string]string{"project://child": "failed", "project://plain": "blocked"}[ref]
			assert.GreaterOrEqual(t, indexContaining(texts, "parent handler: release."+declaring+" node_error"), 0, "%q", texts)
			assert.Equal(t, -1, indexContaining(texts, "after release"), "the parent's graph stopped: %q", texts)
			assert.Empty(t, run.recorded, "a propagated failure is the run's, not a handled one")
		})
	}
}

// propagate_outcome on a child that succeeds changes nothing.
func TestChildDeclaredFailure_PropagateOutcomeOnASuccess(t *testing.T) {
	run := runChildOutcome(t, wrapperFor("project://plain", "    propagate_outcome: true\n"), "ok", nil)
	require.Equal(t, "completed", run.res.Execution.Outcome, "%+v", run.res.Execution.Error)
	assert.GreaterOrEqual(t, indexContaining(savedTexts(run.res), "after release"), 0)
}

// A propagated failure is a failure like any other: tolerate_failure on the
// node tolerates it, records the node, and the run goes on.
func TestChildDeclaredFailure_PropagatedFailureCanBeTolerated(t *testing.T) {
	run := runChildOutcome(t, wrapperFor("project://plain", "    propagate_outcome: true\n    tolerate_failure: true\n"), "fail", nil)
	require.Equal(t, "completed", run.res.Execution.Outcome, "%+v", run.res.Execution.Error)
	assert.GreaterOrEqual(t, indexContaining(savedTexts(run.res), "after release"), 0, "%q", savedTexts(run.res))
	require.Len(t, run.recorded, 1, "%+v", run.recorded)
	assert.Equal(t, "release", run.recorded[0]["step_id"], "the tolerated node is the parent's")
	assert.Contains(t, run.recorded[0]["error"], "release.blocked declared outcome: failure")
}
