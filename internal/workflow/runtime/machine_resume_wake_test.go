// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The 2026-10-20 incident, end to end through the real DynamicWorkflow: every
// run on the account hit its provider's usage limit and parked; with no tool
// traffic the idle policy suspended the machine (correctly); when the limit
// reset the runs resumed straight into tool calls, which never wake a machine
// — so they spun for hours until a human woke it.
//
// The workflow now wakes the machine at the resume point. These drive the
// agent loop with a machine that the idle policy suspends after
// fakeIdleTimeout without tool traffic, and that only MachineResumeWake can
// wake: tool calls against it fail exactly like the real router's do.

// machineWorkflowYAML is the agent loop with a workflow-level daemon, so the
// run needs its machine (RequiresDaemon) and gets a resume waker.
const machineWorkflowYAML = `
name: agent
entry: [agent_loop]
daemon:
  id: daemon-1
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
        - id: execute_tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0
              label: "has_tools"
`

const fakeIdleTimeout = 30 * time.Minute

// fakeMachine is the user's machine as the idle detector and the router see
// it: it suspends after fakeIdleTimeout without tool traffic, a tool call
// against a suspended machine fails (and never wakes it), and a wake brings
// it back.
type fakeMachine struct {
	mu           sync.Mutex
	now          func() time.Time
	lastActivity time.Time
	asleep       bool
	wakes        int
	wakeChecks   int
	toolRuns     []string // "ok" | "asleep"
	events       *[]string
	eventsMu     *sync.Mutex
}

func (m *fakeMachine) settle() {
	if !m.asleep && m.now().Sub(m.lastActivity) >= fakeIdleTimeout {
		m.asleep = true
	}
}

func (m *fakeMachine) event(e string) {
	m.eventsMu.Lock()
	*m.events = append(*m.events, e)
	m.eventsMu.Unlock()
}

func (m *fakeMachine) executeTools(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settle()
	if m.asleep {
		m.toolRuns = append(m.toolRuns, "asleep")
		m.event("tools:asleep")
		return map[string]interface{}{"tool_results": []interface{}{map[string]interface{}{
			"tool_call_id": "tc", "is_error": true,
			"content": "no daemon connected: the machine is asleep (suspended)",
		}}}, nil
	}
	m.lastActivity = m.now()
	m.toolRuns = append(m.toolRuns, "ok")
	m.event("tools:ok")
	return map[string]interface{}{"tool_results": []interface{}{}}, nil
}

func (m *fakeMachine) resumeWake(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wakeChecks++
	m.settle()
	if !m.asleep {
		m.event("wake:noop")
		return map[string]interface{}{"ready": true}, nil
	}
	if names, _ := in["tool_names"].([]interface{}); len(names) == 0 {
		panic(fmt.Sprintf("the wake was not told which tools the batch runs: %v", in))
	}
	m.asleep = false
	m.lastActivity = m.now()
	m.wakes++
	m.event("wake:woke")
	return map[string]interface{}{"ready": true, "woke": true}, nil
}

type MachineResumeWakeSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestMachineResumeWake(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(MachineResumeWakeSuite))
}

func (s *MachineResumeWakeSuite) setup(chatID string) (*subAgentFailureEnv, *fakeMachine, WorkflowInput) {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.workflowYAMLs = map[string]string{"agent": machineWorkflowYAML}

	m := &fakeMachine{now: env.Now, lastActivity: env.Now(), events: &e.events, eventsMu: &e.mu}
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(m.resumeWake, activity.RegisterOptions{Name: "MachineResumeWake"})
	env.OnActivity("ExecuteTools", mock.Anything, mock.Anything).Return(m.executeTools)
	return e, m, input
}

func shellCall(id string) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": "shell", "input": `{"command":"ls"}`}
}

// A run held on a usage limit for two hours: the machine suspends during the
// hold, and when the limit resets the run's next tool call succeeds — with no
// user message, no signal, nothing but the run itself. The wake happens once,
// after the retried LLM call and before the tool batch.
func (s *MachineResumeWakeSuite) TestHeldRunWakesItsMachineOnResume() {
	e, m, input := s.setup("chat-wake-on-resume")
	resetAt := e.env.Now().Add(2 * time.Hour)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		switch turn {
		case 1:
			return []map[string]interface{}{shellCall("tc1")}, nil
		case 2:
			return nil, usageLimitError(resetAt)
		case 3:
			return []map[string]interface{}{shellCall("tc2")}, nil
		default:
			return nil, nil
		}
	}

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal([]string{"ok", "ok"}, m.toolRuns, "the first tool call after the hold ran against an awake machine")
	s.Equal(1, m.wakes, "the machine is woken exactly once, at the resume")
	s.Empty(e.pausedStatuses(), "nothing paused")

	log := strings.Join(e.eventLog(), " ")
	s.Contains(log, "callllm:parent#3 wake:woke tools:ok",
		"the wake runs after the resumed LLM call and before its tool batch")
}

// A run that never leaves its machine idle does no wake at all: back-to-back
// turns cost no activity, so the common case pays nothing.
func (s *MachineResumeWakeSuite) TestBusyRunNeverChecks() {
	e, m, input := s.setup("chat-wake-busy")
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn <= 3 {
			return []map[string]interface{}{shellCall(fmt.Sprintf("tc%d", turn))}, nil
		}
		return nil, nil
	}

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal([]string{"ok", "ok", "ok"}, m.toolRuns)
	s.Zero(m.wakeChecks, "no quiet stretch, no wake check")
}

// A short hold that cannot have idled the machine out still checks past the
// threshold, finds it up, and wakes nothing.
func (s *MachineResumeWakeSuite) TestHoldShorterThanIdleTimeoutWakesNothing() {
	e, m, input := s.setup("chat-wake-short-hold")
	resetAt := e.env.Now().Add(10 * time.Minute)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		switch turn {
		case 1:
			return []map[string]interface{}{shellCall("tc1")}, nil
		case 2:
			return nil, usageLimitError(resetAt)
		case 3:
			return []map[string]interface{}{shellCall("tc2")}, nil
		default:
			return nil, nil
		}
	}

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal([]string{"ok", "ok"}, m.toolRuns)
	s.Equal(1, m.wakeChecks, "past the threshold the machine is checked once")
	s.Zero(m.wakes, "and an awake machine is not woken")
}

// Two sub-agents held on the same usage limit resume together: they share one
// wake (the second waits on the first's), and both tool calls find the machine
// awake.
func (s *MachineResumeWakeSuite) TestSubAgentsResumingTogetherShareOneWake() {
	e, m, input := s.setup("chat-wake-subagents")
	resetAt := e.env.Now().Add(3 * time.Hour)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("sa1", "one"), spawnToolCall("sa2", "two")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, turn int) (map[string]interface{}, error) {
		switch turn {
		case 1:
			return nil, usageLimitError(resetAt)
		case 2:
			return map[string]interface{}{"response_text": "working", "tool_calls": toolCallsJSON(s.T(),
				[]map[string]interface{}{shellCall(toolCallID + "-shell")})}, nil
		default:
			return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
		}
	}

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	s.Equal(1, m.wakes, "one wake for both sub-agents")
	s.Equal(1, m.wakeChecks, "the second sub-agent waited on the first's wake instead of starting its own")
	s.Equal([]string{"ok", "ok"}, m.toolRuns, "both sub-agents' tool calls ran against an awake machine")
}
