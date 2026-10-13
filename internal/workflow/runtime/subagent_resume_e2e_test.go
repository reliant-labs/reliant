// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A sub-agent that stopped on a failure is not finished: a message sent to it
// resumes it where it stopped, and that message is what its next turn reads.
//
// Observed in prod (workflow 9fbaace8, 2026-10-13): a sub-agent hit the
// provider's 7-day usage limit, ended with status failed, and every later
// message to it — the user's, from the UI — was refused with "This agent has
// already finished". A transient condition had terminally killed the thread.
//
// These run the whole DynamicWorkflow with the mailbox modelled the way
// production has it: SendAgentMessage / spawn_send queue a row and ring the
// thread's doorbell, and the agent's next CallLLM drains the row.

// subAgentResumeYAML is spawnBackgroundE2EYAML with call_llm reporting the
// model it was dispatched with, through an arg the stub can read back.
const subAgentResumeYAML = `
name: agent
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
            thinking_level: "{{has(inputs.model) ? inputs.model : 'preset-model'}}"
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

// milestoneResumedAgentSettled is reached once the failing agent (tc1) has
// written "completed" to its own workflow row: the resumed run has finished.
const milestoneResumedAgentSettled = "tc1-settled-completed"

// subAgentResumeEnv is the spawn e2e env with the mailbox, the agents' error
// rows and their reports modelled. The parent's first turn spawns tc1 (the
// agent that fails) and, when withBystander is set, tc2 — a sibling held open
// so the run is still live when the failed agent is messaged, which is the
// shape of the prod chat: an orchestrator still running its other agents.
type subAgentResumeEnv struct {
	*spawnE2EEnv
	mailbox *fakeMailbox

	// failTurns makes the failing agent's turn N return this error.
	failTurns map[int]error
	// onFailingAgentTurn runs at the start of the failing agent's turn N,
	// before it returns; onParentTurn likewise for the parent.
	onFailingAgentTurn map[int]func(agentThread string)
	onParentTurn       map[int]func(agentThread string)
	// parentToolCalls scripts the parent's turn N (after the first, which
	// spawns), given the failing agent's thread.
	parentToolCalls map[int]func(agentThread string) []map[string]interface{}
	// ownershipChecks counts ValidateThreadOwnership: the first thing a
	// spawn(agent_id=...) resume does once it is allowed to proceed.
	ownershipChecks int
	// failingAgentWaitsFor holds the failing agent's turn N until a milestone.
	failingAgentWaitsFor map[int]string
	// bystanderWaitsFor holds the bystander's one turn until a milestone, or
	// until bystanderTimeout passes — so a run that never reaches it still
	// ends and the assertions, not a hang, report what happened.
	bystanderWaitsFor string
	bystanderTimeout  time.Duration

	// Recorded, guarded by spawnE2EEnv.mu.
	parentWorkflowID string
	turns            map[string]int
	readByTurn       map[string][][]string // per thread: mailbox rows delivered by each turn's drain
	modelByTurn      map[string][]string   // per thread: the model each turn was dispatched with
	errorRows        []map[string]interface{}
	reports          []map[string]interface{}
}

func newSubAgentResumeEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, withBystander bool) *subAgentResumeEnv {
	t.Helper()
	spawns := []map[string]interface{}{spawnToolCall("tc1", "do the risky part")}
	if withBystander {
		spawns = append(spawns, spawnToolCall("tc2", "do the long part"))
	}
	base := newSpawnE2EEnv(t, env, []scriptedToolCallsResponse{{toolCalls: spawns}})
	e := &subAgentResumeEnv{
		spawnE2EEnv:          base,
		mailbox:              &fakeMailbox{queued: map[string][]string{}, delivered: map[string][]string{}},
		failTurns:            map[int]error{},
		onFailingAgentTurn:   map[int]func(string){},
		onParentTurn:         map[int]func(string){},
		parentToolCalls:      map[int]func(string) []map[string]interface{}{},
		failingAgentWaitsFor: map[int]string{},
		bystanderTimeout:     3 * time.Second,
		turns:                map[string]int{},
		readByTurn:           map[string][][]string{},
		modelByTurn:          map[string][]string{},
	}

	// The test environment keeps the last registration for a name, so these
	// replace the base env's stubs.
	wf, err := wfyaml.ParseWorkflow([]byte(subAgentResumeYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLM(ctx, input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.errorRows = append(e.errorRows, input)
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowError"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			toThread, _ := input["to_thread_id"].(string)
			toolCallID, _ := input["tool_call_id"].(string)
			e.mailbox.enqueue(toThread, fmt.Sprintf("report:%s", toolCallID))
			e.mu.Lock()
			e.reports = append(e.reports, input)
			e.events = append(e.events, "report:"+toolCallID)
			e.mu.Unlock()
			return map[string]interface{}{"id": "am-" + toolCallID}, nil
		},
		activity.RegisterOptions{Name: "EnqueueAgentMessage"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.statuses = append(e.statuses, input)
			if tc, _ := input["spawned_by_tool_call_id"].(string); tc != "" {
				e.events = append(e.events, fmt.Sprintf("child-status:%s:%v", tc, input["status"]))
				if tc == "tc1" && input["status"] == "completed" {
					e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneResumedAgentSettled
				}
			}
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.toolStatuses = append(e.toolStatuses, input)
			e.events = append(e.events, fmt.Sprintf("tool-status:%v:%v", input["tool_call_id"], input["status"]))
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitToolCallStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			e.mu.Lock()
			e.ownershipChecks++
			e.mu.Unlock()
			return map[string]interface{}{"valid": true}, nil
		},
		activity.RegisterOptions{Name: "ValidateThreadOwnership"},
	)
	return e
}

// agentThread is the thread spawn tool call toolCallID runs on.
func (e *subAgentResumeEnv) agentThread(toolCallID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.agentThreadLocked(toolCallID)
}

func (e *subAgentResumeEnv) agentThreadLocked(toolCallID string) string {
	if e.parentWorkflowID == "" {
		return ""
	}
	return DeterministicWorkflowID(e.parentWorkflowID, toolCallID)
}

// callLLM is CallLLMActivity's shape: drain the thread's mailbox, then take the
// turn. The parent's first turn spawns; every other turn has nothing to do.
func (e *subAgentResumeEnv) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mailbox.drain(thread)

	e.mu.Lock()
	isParent := thread == e.parentThread
	if isParent && e.parentWorkflowID == "" {
		e.parentWorkflowID = input.Runtime.WorkflowID
	}
	failingAgent := e.agentThreadLocked("tc1")
	bystander := e.agentThreadLocked("tc2")
	e.turns[thread]++
	turn := e.turns[thread]
	e.callLLMByThread[thread]++
	delivered, _ := e.mailbox.snapshot(thread)
	var newlyRead []string
	for _, row := range delivered {
		if !slices.ContainsFunc(e.readByTurn[thread], func(seen []string) bool { return slices.Contains(seen, row) }) {
			newlyRead = append(newlyRead, row)
		}
	}
	e.readByTurn[thread] = append(e.readByTurn[thread], newlyRead)
	e.modelByTurn[thread] = append(e.modelByTurn[thread], input.Node.GetCallLlm().GetThinkingLevel().GetLiteral())

	var hook func(string)
	var waitFor string
	var fail error
	var scripted func(string) []map[string]interface{}
	switch thread {
	case e.parentThread:
		hook = e.onParentTurn[turn]
		scripted = e.parentToolCalls[turn]
		e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneParentTurnDelivered(turn)
	case failingAgent:
		hook = e.onFailingAgentTurn[turn]
		waitFor = e.failingAgentWaitsFor[turn]
		fail = e.failTurns[turn]
	case bystander:
		waitFor = e.bystanderWaitsFor
	}
	e.mu.Unlock()

	if waitFor != "" {
		if thread == bystander {
			e.awaitMilestoneFor(waitFor, e.bystanderTimeout)
		} else {
			e.awaitMilestone(waitFor)
		}
	}
	if hook != nil {
		hook(failingAgent)
	}
	if fail != nil {
		return nil, fail
	}
	if scripted != nil {
		toolCallsJSON, err := json.Marshal(scripted(failingAgent))
		require.NoError(e.t, err)
		var toolCalls []interface{}
		require.NoError(e.t, json.Unmarshal(toolCallsJSON, &toolCalls))
		return map[string]interface{}{"response_text": "working", "tool_calls": toolCalls}, nil
	}
	if isParent && turn == 1 {
		toolCallsJSON, err := json.Marshal(e.script[0].toolCalls)
		require.NoError(e.t, err)
		var toolCalls []interface{}
		require.NoError(e.t, json.Unmarshal(toolCallsJSON, &toolCalls))
		return map[string]interface{}{"response_text": "working", "tool_calls": toolCalls}, nil
	}
	return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
}

// awaitMilestoneFor is awaitMilestone with a deadline.
func (e *subAgentResumeEnv) awaitMilestoneFor(name string, timeout time.Duration) {
	e.mu.Lock()
	ch := e.milestone(name)
	e.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
	case <-e.testDone:
	}
}

// messageAgent is SendAgentMessage (and spawn_send): queue a row for the
// agent, then ring its doorbell.
func (e *subAgentResumeEnv) messageAgent(agentThread, text string) {
	e.mailbox.enqueue(agentThread, text)
	e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: agentThread, Reason: threadwake.ReasonMailbox})
}

// changeAgentModel is UpdateWorkflowParams scoped to the agent's thread.
func (e *subAgentResumeEnv) changeAgentModel(agentThread, model string) {
	e.env.SignalWorkflow("update_workflow_state", map[string]interface{}{"__thread": agentThread, "model": model})
}

func (e *subAgentResumeEnv) agentTurns(toolCallID string) int {
	thread := e.agentThread(toolCallID)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.turns[thread]
}

// readOnTurn is what the agent's turn N delivered from its mailbox.
func (e *subAgentResumeEnv) readOnTurn(toolCallID string, turn int) []string {
	thread := e.agentThread(toolCallID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if turn > len(e.readByTurn[thread]) {
		return nil
	}
	return e.readByTurn[thread][turn-1]
}

func (e *subAgentResumeEnv) modelsOf(toolCallID string) []string {
	thread := e.agentThread(toolCallID)
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.modelByTurn[thread])
}

// reportsFrom is every mailbox report the agent wrote to its parent, in order:
// "<kind>@<tool_call_id>".
func (e *subAgentResumeEnv) reportsFrom(toolCallID string) []string {
	thread := e.agentThread(toolCallID)
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, r := range e.reports {
		if r["from_thread_id"] == thread {
			out = append(out, fmt.Sprintf("%v@%v", r["kind"], r["tool_call_id"]))
		}
	}
	return out
}

func (e *subAgentResumeEnv) reportBodiesFrom(toolCallID string) []string {
	thread := e.agentThread(toolCallID)
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, r := range e.reports {
		if r["from_thread_id"] == thread {
			body, _ := r["body"].(string)
			out = append(out, body)
		}
	}
	return out
}

// errorSummariesOn is the summary of every error row written to thread.
func (e *subAgentResumeEnv) errorSummariesOn(thread string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, row := range e.errorRows {
		if row["thread"] == thread {
			summary, _ := row["error_summary"].(string)
			out = append(out, summary)
		}
	}
	return out
}

// toolStatusesOf is every status the workflow wrote for a spawn tool call.
func (e *subAgentResumeEnv) toolStatusesOf(toolCallID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	prefix := "tool-status:" + toolCallID + ":"
	for _, ev := range e.events {
		if s, ok := strings.CutPrefix(ev, prefix); ok {
			out = append(out, s)
		}
	}
	return out
}

// childStatuses is the agent's workflow-row lifecycle as the workflow wrote it.
func (e *subAgentResumeEnv) childStatuses(toolCallID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	prefix := "child-status:" + toolCallID + ":"
	for _, ev := range e.events {
		if s, ok := strings.CutPrefix(ev, prefix); ok {
			out = append(out, s)
		}
	}
	return out
}

type SubAgentResumeE2ESuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSubAgentResumeE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SubAgentResumeE2ESuite))
}

// The prod shape: a sub-agent hits a provider usage limit and stops; its
// sibling keeps the run going; the user messages the stopped agent. The
// message resumes it, its next CallLLM reads the message, and it reports its
// new outcome to its parent under a fresh slot — the original spawn's one
// report slot already holds the failure.
func (s *SubAgentResumeE2ESuite) TestStoppedSubAgentIsResumedByAMessageAndReadsItNextTurn() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)
	const msg = "the limit is on Anthropic only: carry on"

	// The agent fails once the parent is parked on its spawns, so the
	// parent's next turn is the one that reacts to the failure.
	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(usageLimitErr())
	// That turn is where the user, having read the error, messages the
	// stopped agent.
	e.onParentTurn[3] = func(agent string) { e.messageAgent(agent, msg) }
	// The sibling keeps the run live until the resumed agent has finished.
	e.bystanderWaitsFor = milestoneResumedAgentSettled

	e.execute("chat-resume-on-message")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().Equal(2, e.agentTurns("tc1"),
		"STRANDED: the message to the stopped agent never resumed it — it took only the turn that failed")
	s.Require().Equal([]string{msg}, e.readOnTurn("tc1", 2),
		"the resumed agent's first CallLLM must deliver the message that resumed it")
	_, queued := e.mailbox.snapshot(e.agentThread("tc1"))
	s.Require().Empty(queued)

	reports := e.reportsFrom("tc1")
	s.Require().Len(reports, 2, "one report for the failure, one for the resumed run: %v", reports)
	s.Require().Equal("4@tc1", reports[0], "the failure is reported under the spawn's own tool call")
	s.Require().True(strings.HasPrefix(reports[1], "2@tc1#resume-"),
		"the resumed run reports its completion under a fresh slot, not the failure's: %v", reports)

	s.Require().Equal([]string{"started", "failed", "started", "completed"}, e.childStatuses("tc1"),
		"the agent's own workflow row goes back to running for the resumed run")
	s.Require().Equal([]string{"backgrounded", "failed"}, e.toolStatusesOf("tc1"),
		"the spawn call ended once, with the failure; the resumed run is the agent's, not the call's")
}

// The user switches the stopped agent to another provider's model before
// messaging it. The resume must run on that model, not on the one that hit
// the limit — the same rule syncIterationInputs applies to a resumed main
// thread.
func (s *SubAgentResumeE2ESuite) TestResumeRunsOnTheModelChosenWhileStopped() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)

	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(usageLimitErr())
	e.onParentTurn[3] = func(agent string) {
		e.changeAgentModel(agent, "gpt-5@openai")
		e.messageAgent(agent, "switched you to OpenAI, continue")
	}
	e.bystanderWaitsFor = milestoneResumedAgentSettled

	e.execute("chat-resume-new-model")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal([]string{"preset-model", "gpt-5@openai"}, e.modelsOf("tc1"),
		"the resumed turn must use the model the user picked while the agent was stopped")
}

// A message that reaches the agent while its failing step is still in flight
// is unread when the step gives up. The agent takes the turn for it instead
// of ending — and its parent, which never saw a failure, gets one report.
func (s *SubAgentResumeE2ESuite) TestMessageThatArrivesWhileTheStepFailsGetsATurn() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const msg = "try the cached copy instead"

	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.onFailingAgentTurn[1] = func(agent string) { e.messageAgent(agent, msg) }
	e.failTurns[1] = classifyError(usageLimitErr())

	e.execute("chat-message-during-failure")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(2, e.agentTurns("tc1"),
		"STRANDED: the agent ended with a message it never read")
	s.Require().Equal([]string{msg}, e.readOnTurn("tc1", 2))
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"),
		"the agent recovered, so its parent hears one completion and no failure")
}

// A provider usage limit fails the step on its first attempt — retrying a
// window measured in days only delays the error — and what the user and the
// parent are told names the reset time and how to continue.
func (s *SubAgentResumeE2ESuite) TestUsageLimitStopsTheAgentAtOnceAndSaysHowToResume() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(usageLimitErr())

	e.execute("chat-usage-limit")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(1, e.agentTurns("tc1"), "a usage limit must not be retried")
	s.Require().Equal([]string{"4@tc1"}, e.reportsFrom("tc1"))

	summaries := e.errorSummariesOn(e.agentThread("tc1"))
	s.Require().Len(summaries, 1, "the agent's own thread shows the error once")
	s.Require().Contains(summaries[0], "resets Tue Sep 29 20:00 UTC")
	s.Require().Contains(summaries[0], "switch this agent to a model from another provider",
		"the user must be told how to continue: %q", summaries[0])
	s.Require().Contains(summaries[0], "send it a message to resume it", "got %q", summaries[0])

	body := e.reportBodiesFrom("tc1")[0]
	s.Require().Contains(body, "resets Tue Sep 29 20:00 UTC")
	s.Require().Contains(body, "provider usage limit",
		"the parent must know resuming on the same provider fails until the reset: %q", body)
	s.Require().Contains(body, "spawn_send", "the parent must know a message resumes it: %q", body)
}

// The parent acts on the failure report it got earlier and asks to resume the
// agent — after a message has already resumed it. One agent is one loop on
// one thread: the parent is told it is running, and no second loop starts.
func (s *SubAgentResumeE2ESuite) TestParentCannotResumeAnAgentAMessageAlreadyResumed() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)

	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(usageLimitErr())
	e.onParentTurn[3] = func(agent string) { e.messageAgent(agent, "carry on") }
	e.parentToolCalls[3] = func(agent string) []map[string]interface{} {
		input, _ := json.Marshal(map[string]interface{}{"agent_id": agent, "prompt": "retry it", "preset": "general"})
		return []map[string]interface{}{{"id": "tc3", "name": "spawn", "input": string(input)}}
	}
	e.bystanderWaitsFor = milestoneResumedAgentSettled

	e.execute("chat-resume-twice")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(2, e.agentTurns("tc1"), "the agent runs once more, for the message — not twice")
	e.mu.Lock()
	ownershipChecks := e.ownershipChecks
	e.mu.Unlock()
	s.Require().Zero(ownershipChecks, "the parent's resume of a running agent must be refused before it starts anything")
}

// Control: an agent that COMPLETED is finished. A wake for it — whatever sent
// it — must not start it again.
func (s *SubAgentResumeE2ESuite) TestCompletedSubAgentIsNotRestartedByAMessage() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)

	// tc1 completes; the parent's turn after its report messages it while the
	// sibling still holds the run open.
	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.onParentTurn[3] = func(agent string) { e.messageAgent(agent, "one more thing") }
	e.bystanderWaitsFor = milestoneParentTurnDelivered(3)

	e.execute("chat-completed-not-restarted")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(1, e.agentTurns("tc1"), "a completed agent is finished; a message must not restart it")
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"))
}
