// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// reliant#2752: an optional node (tolerate_failure: true) that fails lets the
// run go on, with nodes.<id>.failed and nodes.<id>.error for what comes after
// it to branch on. `probe` stands in for pr-review's cp_reader.
const toleratedProbeYAML = `
name: agent
entry: [probe]
daemon:
  id: daemon-1
outputs:
  probe_failed: "{{ nodes.probe.?failed.orValue(false) }}"
  probe_error: "{{ nodes.probe.?error.orValue('') }}"
  reported: "{{ nodes.report.?seen.orValue('') }}"
nodes:
  - id: probe
    type: run
    command: "cat missing.txt"
    timeout: 5m
    %s
  - id: report
    type: compute
    outputs:
      seen: "{{ nodes.probe.?failed.orValue(false) ? 'probe failed' : 'probe ok' }}"
edges:
  - from: probe
    default: report
`

func (s *MachineBlipRunSuite) runToleratedProbe(chatID, tolerate string, gate func(n int) (map[string]interface{}, error)) (*subAgentFailureEnv, *gateCalls) {
	e, calls, input := s.setupYAML(chatID, fmt.Sprintf(toleratedProbeYAML, tolerate), true,
		func(_ context.Context, _ map[string]interface{}, n int) (map[string]interface{}, error) {
			return gate(n)
		})
	e.env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().True(e.env.IsWorkflowCompleted())
	return e, calls
}

// The optional node fails, and the run completes past it: the next node runs
// and reads why.
func (s *MachineBlipRunSuite) TestToleratedNodeFailure_RunContinuesWithFailedOutputs() {
	e, calls := s.runToleratedProbe("chat-tolerated-probe", "tolerate_failure: true", func(int) (map[string]interface{}, error) {
		return nil, outcomeUnknown()
	})

	s.Require().NoError(e.env.GetWorkflowError(), "a tolerated node's failure is not the run's")
	var result WorkflowResult
	s.Require().NoError(e.env.GetWorkflowResult(&result))
	s.Equal(true, result.Outputs["probe_failed"])
	s.Contains(result.Outputs["probe_error"], "it may have run", "the node's error is its outputs' error")
	s.Equal("probe failed", result.Outputs["reported"], "the node after it ran and branched on failed")
	s.Equal(1, calls.count(), "a tolerated failure is not retried by a pause and resume")
	s.Empty(e.pausedStatuses(), "the run never pauses on an optional node")
}

// Without the field the same failure is still the run's.
func (s *MachineBlipRunSuite) TestUntoleratedNodeFailure_StillEndsTheRun() {
	e, _ := s.runToleratedProbe("chat-untolerated-probe", "", func(int) (map[string]interface{}, error) {
		return nil, outcomeUnknown()
	})
	s.Require().Error(e.env.GetWorkflowError(), "an ordinary node's failure ends the run")
}

// A node that tolerates failure and succeeds has its ordinary outputs, and no
// failed key.
func (s *MachineBlipRunSuite) TestToleratedNodeSuccess_HasItsOrdinaryOutputs() {
	e, _ := s.runToleratedProbe("chat-tolerated-ok", "tolerate_failure: true", func(int) (map[string]interface{}, error) {
		return gatePassed(), nil
	})
	s.Require().NoError(e.env.GetWorkflowError())
	var result WorkflowResult
	s.Require().NoError(e.env.GetWorkflowResult(&result))
	s.Equal(false, result.Outputs["probe_failed"])
	s.Equal("probe ok", result.Outputs["reported"])
}

func TestToleratedFailure(t *testing.T) {
	t.Parallel()
	tolerant := &reliantv1.Node{Id: "probe", TolerateFailure: true}
	boom := errors.New("github__repo_get_tree: GitHub returned not found\nsecond line")

	out, ok := toleratedFailure(tolerant, boom)
	assert.True(t, ok)
	assert.Equal(t, true, out["failed"])
	assert.Equal(t, oneLineError(boom), out["error"], "one line, as failure.error is")

	_, ok = toleratedFailure(&reliantv1.Node{Id: "probe"}, boom)
	assert.False(t, ok, "unset keeps the node's failure the run's")
	_, ok = toleratedFailure(tolerant, nil)
	assert.False(t, ok)

	// Not failures of the node: a cancel or pause, a panic.
	_, ok = toleratedFailure(tolerant, temporal.NewCanceledError())
	assert.False(t, ok, "a cancel or pause is never tolerated")
	_, ok = toleratedFailure(tolerant, fmt.Errorf("wrapped: %w", &WorkflowPanicError{}))
	assert.False(t, ok, "a panic is a bug, never tolerated")
}

// pr-review's cp_reader (reliant#2752): an optional reader agent whose tool
// calls keep failing identically is ended by the repeated-tool-error breaker.
// That failure is decided in the workflow — every ExecuteTools activity of the
// agent succeeded — so nothing had recorded it, and the run read as having
// succeeded. Tolerating it now records the reader's failed attempt, which is
// what the run's outcome and its diagnostics read.
const toleratedReaderYAML = `
name: agent
entry: [reader]
daemon:
  id: daemon-1
outputs:
  reader_failed: "{{ nodes.reader.?failed.orValue(false) }}"
  reader_error: "{{ nodes.reader.?error.orValue('') }}"
  after: "{{ nodes.after.?seen.orValue('') }}"
nodes:
  - id: reader
    type: workflow
    ref: autopilot-agent
    tolerate_failure: true
    args:
      response_tool_name: submit_review
      max_turns: 40
  - id: after
    type: compute
    outputs:
      seen: "{{ nodes.reader.?failed.orValue(false) ? 'reader failed' : 'reader ok' }}"
edges:
  - from: reader
    default: after
`

func (s *MachineBlipRunSuite) TestToleratedAgentEndedByTheBreaker_RecordsItsFailedAttempt() {
	e, _, input := s.setupYAML("chat-tolerated-reader", toleratedReaderYAML, true,
		func(context.Context, map[string]interface{}, int) (map[string]interface{}, error) {
			return gatePassed(), nil
		})
	child, err := os.ReadFile("../builtin/autopilot-agent.yaml")
	s.Require().NoError(err)
	e.workflowYAMLs["autopilot-agent"] = string(child)

	// Every turn asks for the same tree, and every answer is the same 404.
	call := []map[string]interface{}{{"id": "tc", "name": "github__repo_get_tree", "input": `{"path":"cmd/missing"}`}}
	e.parentTurn = func(int) ([]map[string]interface{}, error) { return call, nil }
	e.childTurn = func(string, int) (map[string]interface{}, error) {
		return map[string]interface{}{"response_text": "", "stop_reason": "tool_use", "tool_calls": toolCallsJSON(s.T(), call)}, nil
	}
	var executeTools int
	e.env.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]interface{}, error) {
		e.mu.Lock()
		executeTools++
		e.mu.Unlock()
		return map[string]interface{}{
			"tool_results": []interface{}{map[string]interface{}{
				"tool_call_id": "tc", "name": "github__repo_get_tree", "is_error": true, "content": "GitHub returned not found",
			}},
			"response_data": map[string]interface{}{"submit_review": nil},
		}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools", DisableAlreadyRegisteredCheck: true})
	var recorded []map[string]interface{}
	e.env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		recorded = append(recorded, in)
		e.mu.Unlock()
		msg, _ := in["error"].(string)
		return nil, temporal.NewNonRetryableApplicationError(msg, "ToleratedFailure", nil)
	}, activity.RegisterOptions{Name: "RecordToleratedFailure"})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError(), "the reader is optional: its failure is not the run's")
	var result WorkflowResult
	s.Require().NoError(e.env.GetWorkflowResult(&result))
	s.Equal(true, result.Outputs["reader_failed"])
	s.Contains(result.Outputs["reader_error"], "failed 3 times in a row with the same error")
	s.Equal("reader failed", result.Outputs["after"])
	s.Equal(RepeatedToolErrorLimit, executeTools, "the breaker, not max_turns, ended the reader")

	// Exactly the reader's failed attempt is on record, under the reader's
	// own node, with the breaker's reason: the inner execute_tools attempts
	// all succeeded, so this row is the only record that a node failed.
	s.Require().Len(recorded, 1, "one failed attempt, for the tolerated node")
	rec := recorded[0]
	s.Equal("reader", rec["step_id"])
	s.Equal("reader", rec["node_path"])
	s.NotEmpty(rec["workflow_id"])
	s.Contains(rec["error"], "failed 3 times in a row with the same error")
	s.NotContains(rec, "chat_id", "a tolerated failure is not reported to the chat as an error")
}

// A tolerated failure that an activity reported already has its failed
// attempt (the activity wrapper wrote it), so nothing more is recorded.
func (s *MachineBlipRunSuite) TestToleratedActivityFailure_RecordsNothingMore() {
	e, _, input := s.setupYAML("chat-tolerated-activity", fmt.Sprintf(toleratedProbeYAML, "tolerate_failure: true"), true,
		func(context.Context, map[string]interface{}, int) (map[string]interface{}, error) {
			return nil, outcomeUnknown()
		})
	var recorded int
	e.env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		recorded++
		e.mu.Unlock()
		return nil, temporal.NewNonRetryableApplicationError("recorded", "ToleratedFailure", nil)
	}, activity.RegisterOptions{Name: "RecordToleratedFailure"})

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().NoError(e.env.GetWorkflowError())
	s.Zero(recorded, "the run step's own failed attempt is the record")
}
