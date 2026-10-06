// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// Whether anyone is attending a run reaches every call_llm it makes, the
// parent's and its spawned sub-agents', as RuntimeContext.Unattended — the
// input the tool capability resolver withholds workflow authoring, standing
// work and mutating integration actions on (tools.UnattendedWithholding). The
// handlers tests prove what the resolver does with it; these prove the full
// DynamicWorkflow hands it over.

type UnattendedCapabilitiesSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestUnattendedCapabilities(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(UnattendedCapabilitiesSuite))
}

// runSpawningAgent runs an agent that spawns one sub-agent and returns the
// RuntimeContext of every call_llm the run made, parent and child.
func (s *UnattendedCapabilitiesSuite) runSpawningAgent(chatID string, inputs map[string]interface{}) *spawnE2EEnv {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "triage the issue")}},
	})
	input := spawnE2EWorkflowInput(chatID)
	input.Inputs = inputs
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	return e
}

// subAgentTurns counts the call_llm turns a spawned sub-agent made: they run
// on their own thread, not the root run's.
func subAgentTurns(e *spawnE2EEnv, rootThread string) int {
	n := 0
	for _, rtx := range e.callLLMRuntimes {
		if rtx.Thread != rootThread {
			n++
		}
	}
	return n
}

// A trigger-fired run's every call_llm is unattended — including its
// sub-agent's, which the spawn inherits rather than declares.
func (s *UnattendedCapabilitiesSuite) TestUnattendedRunsSubAgentInheritsItsCallLLM() {
	const chatID = "chat-unattended-webhook"
	e := s.runSpawningAgent(chatID, map[string]interface{}{
		InputKeyUnattended: true,
		InputKeyLaunchRun:  true,
	})

	s.Require().NotEmpty(e.callLLMRuntimes)
	s.Require().Positive(subAgentTurns(e, "thread-"+chatID), "the sub-agent must have taken a turn")
	for i, rtx := range e.callLLMRuntimes {
		s.True(rtx.Unattended, "call_llm #%d (thread %s, spawn depth %d) must be unattended", i, rtx.Thread, rtx.SpawnDepth)
	}
}

// A person's turn in that chat is a new run without the mark, and a chat a
// person started is attended from its launch run on: no call_llm, the
// sub-agent's included, is unattended.
func (s *UnattendedCapabilitiesSuite) TestAttendedRunsCallLLMsAreAttended() {
	for name, inputs := range map[string]map[string]interface{}{
		"a person's turn in an automation's chat": {},
		"a chat a person started":                 {InputKeyLaunchRun: true},
	} {
		s.Run(name, func() {
			const chatID = "chat-attended"
			e := s.runSpawningAgent(chatID, inputs)
			s.Require().Positive(subAgentTurns(e, "thread-"+chatID))
			for i, rtx := range e.callLLMRuntimes {
				s.False(rtx.Unattended, "call_llm #%d (thread %s) must be attended", i, rtx.Thread)
			}
		})
	}
}
