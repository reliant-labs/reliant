// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// An `ask_question` node is the same shape as `approval`: it runs no activity
// named for its node type. It creates a row via QuestionCreate and then blocks
// on signal.question.<id>, which only a human sends.
//
// That makes the mock load-bearing in a way an ordinary activity stub is not.
// While QuestionCreate was stubbed to an empty map the workflow waited for a
// signal nobody sends, the test environment auto-fired the node's 24h timer,
// and the question resolved as a TIMEOUT — so `has_feedback: true` in a
// scenario silently became "the user never answered", every time. The agent
// loop then exited after one iteration and the scenario failed against a
// runtime that was behaving correctly.
//
// Production does loop here, which is what says the harness was at fault: the
// recorded history in replaytest/fixtures/pause_resume.json shows
// QuestionCreate -> signal.question.<id> carrying freetext feedback -> a second
// loop iteration (WorkflowCheckpoint + CallLLM) -> a second QuestionCreate
// answered "Continue" -> exit.
const askQuestionInLoopYAML = `
name: ask-question-in-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: outputs.has_feedback == true
    inline:
      outputs:
        has_feedback: "{{has(nodes.ask) && has(nodes.ask.has_feedback) ? nodes.ask.has_feedback : false}}"
      entry: [start]
      nodes:
        - id: start
          type: call_llm
        - id: ask
          type: ask_question
      edges:
        - from: start
          default: ask
`

// TestAskQuestion_FeedbackContinuesLoop is the regression guard for the mock:
// feedback on the first turn must drive a SECOND iteration, and the plain
// continue on that turn must end the loop at exactly two.
//
// If QuestionCreate goes back to returning an empty map, the timeout path
// resolves has_feedback=false on iteration 0 and this stops at one iteration.
func TestAskQuestion_FeedbackContinuesLoop(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "ask_question_feedback_continues",
		Events: []simulator.SimulatedEvent{
			{Node: "agent_loop.start", Output: map[string]interface{}{"response_text": "draft"}},
			{Node: "agent_loop.ask", Output: map[string]interface{}{"has_feedback": true}},
			{Node: "agent_loop.start", Output: map[string]interface{}{"response_text": "revised"}},
			{Node: "agent_loop.ask", Output: map[string]interface{}{"has_feedback": false}},
		},
	}
	res := runYAMLScenario(t, askQuestionInLoopYAML, sc)
	t.Logf("ask-in-loop: status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "agent_loop.ask",
		"ask_question inside a loop body must be dispatched inline and entered")
	require.Equal(t, "completed", res.Execution.Outcome)

	// Every event consumed is what proves the second iteration really ran: a
	// loop that exited after one turn leaves the second pair unconsumed.
	require.Empty(t, res.Mismatches,
		"feedback must re-enter the loop, consuming the second iteration's events")

	require.Equal(t,
		map[string]interface{}{model.LoopOutputIterationsField: 2},
		res.Execution.NodeOutputs["agent_loop"],
		"the loop node's iteration count must be observable on this backend")
}

// TestAskQuestion_NoFeedbackExitsLoop pins the other half: without feedback the
// loop must exit after one iteration. Without it, a mock that answered
// "feedback" unconditionally would pass the test above while looping forever.
func TestAskQuestion_NoFeedbackExitsLoop(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "ask_question_no_feedback_exits",
		Events: []simulator.SimulatedEvent{
			{Node: "agent_loop.start", Output: map[string]interface{}{"response_text": "done"}},
			{Node: "agent_loop.ask", Output: map[string]interface{}{"has_feedback": false}},
		},
	}
	res := runYAMLScenario(t, askQuestionInLoopYAML, sc)
	t.Logf("ask-in-loop-exit: status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Equal(t, "completed", res.Execution.Outcome)
	require.Empty(t, res.Mismatches)
	require.Equal(t,
		map[string]interface{}{model.LoopOutputIterationsField: 1},
		res.Execution.NodeOutputs["agent_loop"],
		"a plain continue must end the loop at one iteration")
}
