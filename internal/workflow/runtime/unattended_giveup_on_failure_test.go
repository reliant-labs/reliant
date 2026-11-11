// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// An unattended run whose self-pause outlives its ladder ends FAILED
// (pause_coordinator.go). A failed run gets its on_failure handler; this one
// is no exception, and is the case the handler matters most for: nobody is
// watching, so the handler is the only thing that tells anyone. Seen in
// control-plane's pr-review (run 0e19105a, 2026-11-10): the run failed with
// UnattendedPauseGaveUp and its on_failure — which labels the PR review:error —
// never ran, leaving the PR in the review queue.

type UnattendedGiveUpSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestUnattendedGiveUp(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(UnattendedGiveUpSuite))
}

// The reviewer is a `workflow:` node running the agent loop, as pr-review's is.
const unattendedGiveUpYAML = `
name: guarded-review
inputs:
  unattended: {type: boolean, default: true}
entry: [review]
nodes:
  - id: review
    type: workflow
    inline:
      entry: [agent_loop]
      nodes:
        - id: agent_loop
          type: loop
          while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0)
          inline:
            outputs:
              tool_calls: "{{nodes.call_llm.tool_calls}}"
            entry: [call_llm]
            nodes:
              - id: call_llm
                type: call_llm
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: compute
      outputs:
        why: "{{ failure.error }}"
`

// The same failure through a ref'd reviewer and a sequence handler whose
// first node is skipped, as pr-review is written.
const unattendedGiveUpRefYAML = `
name: guarded-review
inputs:
  unattended: {type: boolean, default: true}
entry: [review]
nodes:
  - id: review
    type: workflow
    ref: reviewer
    presets: {default: codex_limit_fallback}
    thread:
      mode: new
      inject:
        role: user
        content: review it
on_failure:
  sequence:
  - id: now
    type: compute
    condition: "nodes.?check.?valid.orValue(false) == true"
    outputs:
      x: "{{ 1 }}"
  - id: why
    type: compute
    outputs:
      why: "{{ failure.error }}"
`

const unattendedGiveUpReviewerYAML = `
name: reviewer
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0)
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
`

func (s *UnattendedGiveUpSuite) TestGiveUp_RunsOnFailure() {
	s.runGiveUp(map[string]string{"guarded-review": unattendedGiveUpYAML})
}

func (s *UnattendedGiveUpSuite) TestGiveUp_RefReviewer_RunsOnFailure() {
	s.runGiveUp(map[string]string{"guarded-review": unattendedGiveUpRefYAML, "reviewer": unattendedGiveUpReviewerYAML})
}

func (s *UnattendedGiveUpSuite) runGiveUp(yamls map[string]string) {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-giveup-on-failure"
	input := spawnE2EWorkflowInput(chatID)
	input.WorkflowName = "guarded-review"
	input.ExecContext.WorkflowName = "guarded-review"
	input.Inputs["unattended"] = true
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = yamls
	e.failFirstCallOf = func(string) error {
		return temporal.NewNonRetryableApplicationError(
			"failed to resolve model: model gpt-6.1-sol is unavailable: no servable provider", "ProviderUnavailable", nil)
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	s.Require().Error(err)
	s.Contains(err.Error(), unattendedPauseGaveUpErrType, "the run gives up after its self-resume ladder")

	value, qerr := env.QueryWorkflow(OnFailureQuery)
	s.Require().NoError(qerr)
	var record OnFailureRecord
	s.Require().NoError(value.Get(&record))
	s.True(record.Ran, "a run that gave up FAILED, so its on_failure handler runs")
	s.Contains(record.Error, "no servable provider", "and it is told why")
	s.Empty(record.HandlerError, "the handler itself runs cleanly")
}
