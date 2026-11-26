// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A run with a timeout, whose `hold` command outlasts it unless the scenario
// says otherwise. on_failure and finally both report what they saw.
const timeoutYAML = `
name: bounded
timeout: TIMEOUT
entry: [hold]
nodes:
  - id: hold
    type: run
    args:
      command: "sleep 3600"
  - id: after
    type: save_message
    args:
      role: assistant
      content: "done"
edges:
  - from: hold
    default: after
on_failure:
  entry: [noted]
  nodes:
    - id: noted
      type: save_message
      args:
        role: assistant
        content: "on_failure code={{ failure.error_code }} error={{ failure.error }}"
finally:
  entry: [release]
  nodes:
    - id: release
      type: save_message
      args:
        role: assistant
        content: "released status={{ run.status }} code={{ failure.error_code }}"
`

type statusLog struct {
	mu    sync.Mutex
	calls []map[string]interface{}
}

func (l *statusLog) record(in map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, in)
}

// terminal is the root run's last status call.
func (l *statusLog) terminal(t *testing.T) map[string]interface{} {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.calls) - 1; i >= 0; i-- {
		if parent, _ := l.calls[i]["parent_workflow_id"].(string); parent == "" {
			return l.calls[i]
		}
	}
	t.Fatalf("no root WorkflowStatus call: %+v", l.calls)
	return nil
}

func runTimed(t *testing.T, timeout string, hold time.Duration, overrides map[string]versionOverride) (*Result, *statusLog) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(strings.Replace(timeoutYAML, "TIMEOUT", timeout, 1)))
	require.NoError(t, err)
	r := NewRunner(wf)
	r.holdRunStep, r.holdRunStepFor = "hold", hold
	r.versionOverrides = overrides
	r.launchedBy = &runtime.TriggerInfo{Kind: "schedule", TriggerID: "t1", EventID: "e1"}
	statuses := &statusLog{}
	r.onWorkflowStatus = statuses.record
	res := r.Run(&scenario.Scenario{Name: "run", Events: []scenario.SimulatedEvent{
		{Node: "hold", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	return res, statuses
}

// The run outlives its timeout while a command runs: the command is
// cancelled, on_failure and then finally run, and the run ends FAILED with
// the timeout as its error. Its launch event hears "failed" with that error,
// which is what settles the firing; Temporal sees the execution fail, which is
// what frees its trigger-capacity slot.
func TestRunTimeout_ExceededFailsTheRunAfterItsHandlers(t *testing.T) {
	res, statuses := runTimed(t, "300ms", time.Minute, nil)

	require.Equal(t, "error", res.Execution.Outcome, "an exceeded timeout fails the run: %+v", res.Execution)
	require.NotNil(t, res.Execution.Error)
	assert.Contains(t, res.Execution.Error.Message, "run timeout 300ms exceeded")
	assert.Empty(t, savedContent(res, "after"), "nothing after the timed-out command ran")

	texts := savedTexts(res)
	onFailure, finally := indexContaining(texts, "on_failure code=run_timeout"), indexContaining(texts, "released status=failed code=run_timeout")
	require.GreaterOrEqual(t, onFailure, 0, "on_failure runs for a timeout, with its code: %q", texts)
	require.GreaterOrEqual(t, finally, 0, "finally runs, and reads the run as failed: %q", texts)
	assert.Less(t, onFailure, finally, "on_failure first, then finally: %q", texts)
	assert.Contains(t, texts[onFailure], "error=run timeout 300ms exceeded")
	require.NotNil(t, res.Execution.Finally)
	assert.Equal(t, runtime.RunStatusFailed, res.Execution.Finally.Status)

	ending := statuses.terminal(t)
	assert.Equal(t, "failed", ending["status"], "the run's row and launch event record failed: %+v", ending)
	assert.Equal(t, "run timeout 300ms exceeded", ending["error"])
	assert.Equal(t, true, ending["launch_run"], "the launch event is the one settled")
}

// A run that finishes inside its timeout is not touched by it.
func TestRunTimeout_ARunUnderItsTimeoutCompletes(t *testing.T) {
	res, statuses := runTimed(t, "1h", 0, nil)

	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Len(t, savedContent(res, "after"), 1)
	assert.Empty(t, savedContent(res, "on_failure.noted"))
	assert.Contains(t, savedContent(res, "finally.release"), "released status=completed code=")
	assert.Equal(t, "completed", statuses.terminal(t)["status"])
}

// A history recorded before the timeout existed has no marker for it: even if
// its definition declares one, it replays with no timer, and a command that
// outlasts the declared timeout still completes.
func TestRunTimeout_VersionGateKeepsOldRunsUnbounded(t *testing.T) {
	res, _ := runTimed(t, "100ms", 400*time.Millisecond, map[string]versionOverride{
		"workflow-run-timeout": {max: 1, returns: workflow.DefaultVersion},
	})

	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Len(t, savedContent(res, "after"), 1)
	assert.Empty(t, savedContent(res, "on_failure.noted"))
}
