// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A message sent to a sub-agent after its last turn, but before it completes,
// must be answered by that agent (subAgentOwesLateMessageTurn).
//
// Observed in prod (obs b90db1df, "[Reconciler] Resolved orphaned agent
// messages"): a parent's spawn_send reached its sub-agent after the agent's
// final turn had passed its loop-exit gate and before its thread went
// terminal. The sender saw a live agent, queued the row and rang the
// doorbell; the agent completed without another turn, and the reconciler
// later marked the row undelivered.
//
// The window is modelled by the agent's FetchThreadResult: the activity that
// reads its answer once its loop has exited, before it reports to its parent
// and goes terminal.

// newSubAgentLateMessageEnv is the sub-agent resume env (one agent, tc1, that
// completes on its first turn) with onAgentFinishing run inside tc1's first
// FetchThreadResult.
func newSubAgentLateMessageEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, onAgentFinishing func(agentThread string)) *subAgentResumeEnv {
	t.Helper()
	e := newSubAgentResumeEnv(t, env, false)
	fetched := 0
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			thread, _ := input["thread"].(string)
			e.mu.Lock()
			agent := e.agentThreadLocked("tc1")
			first := false
			if thread == agent {
				fetched++
				first = fetched == 1
			}
			e.mu.Unlock()
			if first && onAgentFinishing != nil {
				onAgentFinishing(agent)
			}
			return map[string]interface{}{"content": "agent finished", "is_error": false}, nil
		},
		activity.RegisterOptions{Name: "FetchThreadResult"},
	)
	return e
}

type SubAgentLateMessageE2ESuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSubAgentLateMessageE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SubAgentLateMessageE2ESuite))
}

// The fix: the message that landed after the agent's last turn is drained
// and answered by one more turn of the same agent, which then completes and
// reports to its parent exactly once.
func (s *SubAgentLateMessageE2ESuite) TestMessageAfterTheLastTurnIsAnsweredBeforeTheAgentCompletes() {
	env := s.NewTestWorkflowEnvironment()
	const msg = "also check the second region"
	var e *subAgentResumeEnv
	e = newSubAgentLateMessageEnv(s.T(), env, func(agent string) { e.messageAgent(agent, msg) })

	e.execute("chat-subagent-late-message")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(2, e.agentTurns("tc1"),
		"LOST: the agent completed with a message queued after its last turn")
	s.Require().Equal([]string{msg}, e.readOnTurn("tc1", 2), "the extra turn is the one that reads the message")
	_, queued := e.mailbox.snapshot(e.agentThread("tc1"))
	s.Require().Empty(queued, "nothing is left queued for the agent")
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"), "one completion report, after the extra turn")
	s.Require().Equal([]string{"started", "completed"}, e.childStatuses("tc1"), "the agent goes terminal once")
}

// Control: with no message after its last turn, the agent completes after
// one turn, as before.
func (s *SubAgentLateMessageE2ESuite) TestNoLateMessageCompletesAsBefore() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentLateMessageEnv(s.T(), env, nil)

	e.execute("chat-subagent-no-late-message")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(1, e.agentTurns("tc1"))
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"))
	s.Require().Equal([]string{"started", "completed"}, e.childStatuses("tc1"))
}

// A history recorded before the change (no subagent-late-message-gets-a-turn
// marker) replays the old path: the agent completes after one turn, and the
// message stays queued.
func (s *SubAgentLateMessageE2ESuite) TestHistoryBeforeTheChangeKeepsCompleting() {
	env := s.NewTestWorkflowEnvironment()
	env.OnGetVersion(subAgentLateMessageChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	const msg = "also check the second region"
	var e *subAgentResumeEnv
	e = newSubAgentLateMessageEnv(s.T(), env, func(agent string) { e.messageAgent(agent, msg) })

	e.execute("chat-subagent-late-message-old")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(1, e.agentTurns("tc1"))
	_, queued := e.mailbox.snapshot(e.agentThread("tc1"))
	s.Require().Equal([]string{msg}, queued)
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"))
}

// A stop wins: an agent cancelled as it finishes does not run again for a
// message that arrived with the stop.
func (s *SubAgentLateMessageE2ESuite) TestCancelledAgentDoesNotRunAgain() {
	env := s.NewTestWorkflowEnvironment()
	var e *subAgentResumeEnv
	e = newSubAgentLateMessageEnv(s.T(), env, func(agent string) {
		e.env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: agent})
		e.messageAgent(agent, "never mind")
	})

	e.execute("chat-subagent-late-message-cancelled")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().Equal(1, e.agentTurns("tc1"))
}
