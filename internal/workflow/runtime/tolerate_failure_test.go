// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
