// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A reviewer pinned to a driver (`gpt-6.1-sol@codex`) whose node preset says
// `providers: {on_limit: fallback, fallback: claude-5.5-opus@claude}` must
// continue on the fallback when Codex is usage-limited, not wait out the
// window. control-plane's pr-review is written exactly so (report ce808629).

type PinnedDriverFallbackSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestPinnedDriverFallback(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(PinnedDriverFallbackSuite))
}

const pinnedDriverFallbackYAML = `
name: pinned-review
inputs:
  unattended: {type: boolean, default: true}
  review_model:
    type: model
    default: {id: 'gpt-6.1-sol@codex', thinking_level: high}
entry: [review]
nodes:
  - id: review
    type: workflow
    ref: reviewer
    presets: {default: codex_limit_fallback}
    args:
      model: '{{ inputs.review_model }}'
`

const pinnedDriverReviewerYAML = `
name: reviewer
inputs:
  model:
    type: model
    default: {tags: [flagship]}
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
          args:
            model: "{{ inputs.model }}"
`

const pinnedDriverFallbackModel = "claude-5.5-opus@claude"

// codexUsageLimit is a Codex refusal as the activity hands it back: the
// marker, and the facts it gathered, including the servable fallback.
func codexUsageLimit(resetAt time.Time, facts types.UsageLimitFacts) error {
	msg := chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, resetAt.UTC().Format(time.RFC3339),
		chatmarkers.ProviderUsageLimitLead+" (Codex, 5-hour window) — resets soon")
	return temporal.NewApplicationErrorWithOptions(msg, "ProviderUsageLimit", temporal.ApplicationErrorOptions{
		NonRetryable: true,
		Details:      []interface{}{facts},
	})
}

func (s *PinnedDriverFallbackSuite) TestCodexLimit_ContinuesOnFallback() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-pinned-fallback"
	input := spawnE2EWorkflowInput(chatID)
	input.WorkflowName = "pinned-review"
	input.ExecContext.WorkflowName = "pinned-review"
	input.Inputs["unattended"] = true
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = map[string]string{"pinned-review": pinnedDriverFallbackYAML, "reviewer": pinnedDriverReviewerYAML}
	e.presetParams = map[string]map[string]interface{}{
		"codex_limit_fallback": {InputKeyProviderPolicy: map[string]interface{}{
			"on_limit": "fallback", "fallback_model": pinnedDriverFallbackModel,
		}},
	}

	start := env.Now()
	resetAt := start.Add(5 * time.Hour)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			e.mu.Lock()
			failover := e.callRuntime["parent#1"].Failover
			e.mu.Unlock()
			// The facts the activity reports follow the policy it was given.
			facts := types.UsageLimitFacts{Provider: "codex", ResetAt: resetAt, ProviderEarliestReset: resetAt,
				FallbackModel: failover.FallbackModel}
			return nil, codexUsageLimit(resetAt, facts)
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	e.mu.Lock()
	first, second := e.callRuntime["parent#1"], e.callRuntime["parent#2"]
	e.mu.Unlock()
	s.Equal(pinnedDriverFallbackModel, first.Failover.FallbackModel,
		"the node preset's fallback reaches call_llm though the model pins a driver")
	retriedAt, ok := e.callAt("parent", 2)
	s.Require().True(ok)
	s.True(retriedAt.Before(start.Add(time.Minute)), "the reviewer continues at once, not at the reset (retried %s)", retriedAt.Sub(start))
	s.Equal(pinnedDriverFallbackModel, second.FallbackModel, "on the fallback model")
	s.Empty(e.markers(), "no usage-limit wait was recorded")
}
