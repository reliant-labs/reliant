// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A turn a pause cut off is continued when the run resumes, not yielded as the
// agent's answer (reliant #2759).
//
// The prod shape, chat e1041795 on 2026-11-13: the root thread's call_llm
// failed for good (Anthropic 400 claude_code_version_too_old), which pauses
// the run, and the pause cancelled a sub-agent's call_llm mid-stream. That
// call persisted its partial reply (persistInterruptedTurn). When the user
// resumed, the sub-agent's loop re-dispatched the call, found the partial as
// its history's tail with nothing new to read, and yielded it — the agent
// ended with half a reply ("History ends with an assistant message after the
// mailbox drain", obs ac14924f).
//
// The re-dispatch must say which partial is its own: the id the cancelled
// dispatch streamed under (RuntimeContext.CutOffMessageID), which is the id
// its partial was persisted under. call_llm continues a tail with that id
// (TestCallLLM_PauseCutOffTurnContinues in activities/handlers).

type PauseCutOffTurnSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestPauseCutOffTurn(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(PauseCutOffTurnSuite))
}

func (s *PauseCutOffTurnSuite) TestRootProviderFailure_SubAgentResumesItsCutOffTurn() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-pause-cut-off-turn"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var mu sync.Mutex
	resumed := false
	isResumed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return resumed
	}
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-a", "implement the feature")}, nil
		}
		// The root's provider refuses the request outright until the user
		// resumes: a terminal failure, which pauses the run.
		if !isResumed() {
			return nil, providerBadRequest()
		}
		return nil, nil
	}
	e.childTurn = func(_ string, turn int) (map[string]interface{}, error) {
		if turn == 1 {
			// What the cancelled call would have returned; the workflow
			// never reads a cancelled activity's result.
			return map[string]interface{}{"response_text": "I'll start by reading the", "tool_calls": nil}, nil
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	// The root fails while the sub-agent's first turn is streaming.
	e.hold = func(toolCallID string, turn int) string {
		if toolCallID == "" && turn == 2 {
			return milestoneStarted("tc-a", 1)
		}
		return ""
	}
	e.streamUntilCancelled = func(toolCallID string, turn int) bool {
		return toolCallID == "tc-a" && turn == 1
	}
	env.RegisterDelayedCallback(func() {
		mu.Lock()
		resumed = true
		mu.Unlock()
		env.SignalWorkflow("signal.resume", nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Len(e.pausedStatuses(), 1, "the root's terminal failure paused the run")

	e.mu.Lock()
	defer e.mu.Unlock()
	s.Contains(e.cancelledCalls, "tc-a#1", "the pause cancelled the sub-agent's streaming call")

	cut, ok := e.callRuntime["tc-a#1"]
	s.Require().True(ok)
	redispatch, ok := e.callRuntime["tc-a#2"]
	s.Require().True(ok, "the resume re-dispatched the sub-agent's cut-off call")
	s.Require().NotEmpty(cut.AssistantMessageID, "the cut-off call streamed under a pre-allocated id")
	s.Empty(cut.CutOffMessageID, "the first dispatch cut nothing off")
	s.Equal(cut.AssistantMessageID, redispatch.CutOffMessageID,
		"the re-dispatch names the partial the pause cut off, so call_llm continues it instead of yielding it")
	s.NotEqual(cut.AssistantMessageID, redispatch.AssistantMessageID, "the re-dispatch streams under a fresh id")

	// The root's own call failed rather than being cut off: its retry has
	// no partial to continue.
	for _, key := range []string{"parent#2", "parent#3"} {
		rtx, ok := e.callRuntime[key]
		s.Require().True(ok, key)
		s.Empty(rtx.CutOffMessageID, "%s was not cut off by the pause", key)
	}
}
