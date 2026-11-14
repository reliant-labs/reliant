// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// control-plane pr-review, 2026-10-31: a compute node's expression went over
// the CEL cost limit on a large PR. The step failed through FailStep, the run
// paused like a transient failure, the unattended ladder resumed it into the
// same failure five more times, and it stayed paused: on_failure never ran
// and the run held one of the automation's six capacity slots. Six such runs
// took them all. A step whose config cannot run fails the same way on every
// resume, so the run ends, failed, and runs its on_failure handler.

// stepConfigFailureYAML's node `b` reads a key its upstream does not have.
const stepConfigFailureYAML = `
name: agent
entry: [a]
daemon:
  id: daemon-1
nodes:
  - id: a
    type: compute
    outputs:
      m: "{{ {'x': 1} }}"
  - id: b
    type: %s
edges:
  - from: a
    default: b
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: compute
      outputs:
        seen: "{{ failure.node_id }}"
`

const (
	failingRunNode     = "run\n    command: \"echo {{ nodes.a.m.result }}\""
	failingComputeNode = "compute\n    outputs:\n      all: \"{{ nodes.a.m.result }}\""
)

// registerFailStep stands in for handlers.FailStepActivity, which always fails
// non-retryably with the evaluation error.
func registerFailStep(e *subAgentFailureEnv) {
	e.env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		msg, _ := in["error"].(string)
		return nil, temporal.NewNonRetryableApplicationError("step cannot run as configured: "+msg, "", nil)
	}, activity.RegisterOptions{Name: "FailStep"})
}

func (s *MachineBlipRunSuite) assertStepConfigFailureEndsTheRun(chatID, node string, unattended bool) {
	e, calls, input := s.setupYAML(chatID, fmt.Sprintf(stepConfigFailureYAML, node), unattended,
		func(context.Context, map[string]interface{}, int) (map[string]interface{}, error) {
			return gatePassed(), nil
		})
	registerFailStep(e)

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted(), "the run must end, not park holding its automation's slot")
	err := e.env.GetWorkflowError()
	s.Require().Error(err)
	var appErr *temporal.ApplicationError
	s.Require().ErrorAs(err, &appErr)
	s.Equal(stepConfigFailedErrType, appErr.Type())
	s.Contains(err.Error(), "step b failed")
	s.Contains(err.Error(), "no such key: result", "the failure names the expression's error")
	s.Equal(svcerr.ClassUser, errclass.Classify(err),
		"a misconfigured workflow is its author's error: logged below ERROR, never an obs issue")
	s.Empty(e.pausedStatuses(), "a resume would fail the same way, so the run never pauses")
	s.Equal(0, calls.count(), "the command never runs")

	val, qErr := e.env.QueryWorkflow(OnFailureQuery)
	s.Require().NoError(qErr)
	var rec OnFailureRecord
	s.Require().NoError(val.Get(&rec))
	s.True(rec.Ran, "the on_failure handler runs")
	s.Equal("b", rec.NodeID)
	s.Equal(FailureCodeNodeError, rec.ErrorCode)
}

func (s *MachineBlipRunSuite) TestUnattended_RunNodeConfigFailureEndsTheRun() {
	s.assertStepConfigFailureEndsTheRun("chat-config-run-unattended", failingRunNode, true)
}

func (s *MachineBlipRunSuite) TestUnattended_ComputeNodeConfigFailureEndsTheRun() {
	s.assertStepConfigFailureEndsTheRun("chat-config-compute-unattended", failingComputeNode, true)
}

// An attended run fails too: a person's resume re-evaluates the same
// expression over the same data.
func (s *MachineBlipRunSuite) TestAttended_RunNodeConfigFailureEndsTheRun() {
	s.assertStepConfigFailureEndsTheRun("chat-config-run-attended", failingRunNode, false)
}
