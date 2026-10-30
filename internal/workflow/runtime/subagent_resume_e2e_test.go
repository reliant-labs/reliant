// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A sub-agent that stopped on a failure is not finished: a message sent to it
// resumes it where it stopped, and that message is what its next turn reads.
//
// Observed in prod (workflow 9fbaace8, 2026-10-13): a sub-agent hit a
// provider failure, ended with status failed, and every later
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

// milestoneAgentSettled is reached once the agent spawned by toolCallID has
// written "completed" to its own workflow row.
func milestoneAgentSettled(toolCallID string) string {
	return toolCallID + "-settled-completed"
}

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

	// durableStopped is what LookupStoppedSubAgent answers from: the
	// sub-agents that stopped on a failure as the database records them, by
	// thread. An agent that stopped in an EARLIER execution of the chat is
	// in here and not in this execution's memory.
	durableStopped map[string]*db.StoppedSpawnAgent
	// onRootFinishing runs while the root writes its own "completed" status:
	// the run has left its graph and is ending.
	onRootFinishing func(agentThread string)

	// Recorded, guarded by spawnE2EEnv.mu.
	parentWorkflowID string
	turns            map[string]int
	readByTurn       map[string][][]string // per thread: mailbox rows delivered by each turn's drain
	modelByTurn      map[string][]string   // per thread: the model each turn was dispatched with
	errorRows        []map[string]interface{}
	reports          []map[string]interface{}
	lookups          []string // threads LookupStoppedSubAgent was asked about
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
		durableStopped:       map[string]*db.StoppedSpawnAgent{},
	}

	// The test environment keeps the last registration for a name, so these
	// replace the base env's stubs.
	wf, err := wfyaml.ParseWorkflow([]byte(subAgentResumeYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
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
			tc, _ := input["spawned_by_tool_call_id"].(string)
			if tc != "" {
				e.events = append(e.events, fmt.Sprintf("child-status:%s:%v", tc, input["status"]))
				if input["status"] == "completed" {
					e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneAgentSettled(tc)
				}
			}
			var finishing func(string)
			if tc == "" && input["thread"] == e.parentThread && input["status"] == "completed" {
				finishing, e.onRootFinishing = e.onRootFinishing, nil
			}
			failingAgent := e.agentThreadLocked("tc1")
			e.mu.Unlock()
			if finishing != nil {
				finishing(failingAgent)
			}
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			thread, _ := input["thread"].(string)
			e.mu.Lock()
			defer e.mu.Unlock()
			e.lookups = append(e.lookups, thread)
			if all, _ := input["all"].(bool); all {
				var agents []*db.StoppedSpawnAgent
				for _, agent := range e.durableStopped {
					agents = append(agents, agent)
				}
				slices.SortFunc(agents, func(a, b *db.StoppedSpawnAgent) int { return strings.Compare(a.ChildThreadID, b.ChildThreadID) })
				return map[string]interface{}{"agents": agents}, nil
			}
			if agent := e.durableStopped[thread]; agent != nil {
				return map[string]interface{}{"agent": agent}, nil
			}
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: LookupStoppedSubAgentActivityName},
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

// The prod shape: a sub-agent's provider fails and it stops; its
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
	e.failTurns[1] = classifyError(stoppingFailureErr())
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
	e.failTurns[1] = classifyError(stoppingFailureErr())
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
	e.failTurns[1] = classifyError(stoppingFailureErr())

	e.execute("chat-message-during-failure")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Equal(2, e.agentTurns("tc1"),
		"STRANDED: the agent ended with a message it never read")
	s.Require().Equal([]string{msg}, e.readOnTurn("tc1", 2))
	s.Require().Equal([]string{"2@tc1"}, e.reportsFrom("tc1"),
		"the agent recovered, so its parent hears one completion and no failure")
}

// The parent acts on the failure report it got earlier and asks to resume the
// agent — after a message has already resumed it. One agent is one loop on
// one thread: the parent is told it is running, and no second loop starts.
func (s *SubAgentResumeE2ESuite) TestParentCannotResumeAnAgentAMessageAlreadyResumed() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)

	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(stoppingFailureErr())
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

// threadTurns is how many turns thread took, and what each turn's drain read.
func (e *subAgentResumeEnv) threadTurns(thread string) (int, [][]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.turns[thread], slices.Clone(e.readByTurn[thread])
}

// reportsFromThread is reportsFrom for an agent addressed by its thread.
func (e *subAgentResumeEnv) reportsFromThread(thread string) []string {
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

func (e *subAgentResumeEnv) lookedUp() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.lookups)
}

// stoppedBefore is the durable record of an agent that stopped on a failure
// in an EARLIER execution of chatID: issued by its root, resumed resumes times
// already.
func stoppedBefore(chatID, agentThread, toolCallID string, resumes int) *db.StoppedSpawnAgent {
	root := spawnE2EWorkflowInput(chatID).ExecContext
	return &db.StoppedSpawnAgent{
		ToolCallID:        toolCallID,
		ParentThreadID:    root.Thread,
		ToolInput:         `{"preset":"general","prompt":"do the risky part"}`,
		ChildThreadID:     agentThread,
		ChildWorkflowID:   agentThread,
		IssuingWorkflowID: root.WorkflowID,
		SpawnDepth:        1,
		Resumes:           resumes,
	}
}

// The 9fbaace8 shape, after the run that saw the agent stop is gone: the agent
// stopped in an earlier execution of the chat — one that completed when its
// root's turn ended, one a fresh restart replaced, or one from before stopped
// agents were recorded at all — so THIS execution has no memory of it. A
// message to it must still resume it: the run reads the agent's stopped state
// from the database, and resumes it as if it had seen it stop itself.
func (s *SubAgentResumeE2ESuite) TestAgentThatStoppedInAnEarlierRunIsResumedFromDurableState() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, true)
	const chatID = "chat-durable-stopped"
	const stoppedAgent = "agent-stopped-in-an-earlier-run"
	const msg = "the usage window has reset: carry on"

	e.durableStopped[stoppedAgent] = stoppedBefore(chatID, stoppedAgent, "tc-old", 1)
	// While this run's sibling holds it open, the user messages the agent
	// that stopped before this execution began.
	e.onParentTurn[2] = func(string) { e.messageAgent(stoppedAgent, msg) }
	e.bystanderWaitsFor = milestoneAgentSettled("tc-old")

	e.execute(chatID)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	turns, read := e.threadTurns(stoppedAgent)
	s.Require().Equal(1, turns,
		"STRANDED: a message to an agent that stopped in an earlier execution never resumed it")
	s.Require().Equal([]string{msg}, read[0], "the resumed agent's first turn reads the message that resumed it")
	s.Require().Equal([]string{stoppedAgent}, e.lookedUp(), "the run asks the database once, for the thread it has no record of")
	s.Require().Equal([]string{"2@tc-old#resume-2"}, e.reportsFromThread(stoppedAgent),
		"the resumed run reports past the slots its earlier resumes used")
	s.Require().Equal([]string{"started", "completed"}, e.childStatuses("tc-old"),
		"the agent's own workflow row goes back to running, then records the resumed outcome")
}

// The window #785 left open: a message reaches a stopped agent while the run
// is ending — its root's loop has exited, so nothing would wait on the agent,
// and the run would end with it mid-turn. The run must leave it stopped and
// continue as a successor that resumes it, with the message still unread.
func (s *SubAgentResumeE2ESuite) TestMessageForAStoppedAgentAsTheRunEndsIsLeftToTheSuccessor() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const msg = "carry on with the other provider"

	e.failingAgentWaitsFor[1] = milestoneParentTurnDelivered(2)
	e.failTurns[1] = classifyError(stoppingFailureErr())
	e.onRootFinishing = func(agent string) { e.messageAgent(agent, msg) }

	e.execute("chat-stopped-agent-at-run-end")

	s.Require().True(env.IsWorkflowCompleted())
	var contErr *workflow.ContinueAsNewError
	s.Require().True(errors.As(env.GetWorkflowError(), &contErr),
		"STRANDED: the run completed with a message waiting for a stopped agent; got %v", env.GetWorkflowError())
	var successor WorkflowInput
	s.Require().NoError(converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &successor))

	agent := e.agentThread("tc1")
	s.Require().NotNil(successor.Resume, "the successor resumes the chat's run rather than starting it over")
	s.Require().Len(successor.Resume.ResumeStopped, 1)
	resumed := successor.Resume.ResumeStopped[0]
	s.Require().Equal("tc1", resumed.ToolCallID)
	s.Require().Equal(agent, resumed.ChildThread)
	s.Require().Equal(e.parentThread, resumed.ParentThread)
	s.Require().True(successor.Resume.AwaitSpawnsFirst,
		"the successor's root waits on the agent before its first turn — that turn reads the agent's report")

	s.Require().Equal(1, e.agentTurns("tc1"), "the ending run must not start the agent itself")
	_, queued := e.mailbox.snapshot(agent)
	s.Require().Equal([]string{msg}, queued, "the message is the successor's agent's to read")
}

// The run a stopped agent's message starts — by SendAgentMessage for a chat
// whose run had ended, or by the end-of-run check above: it resumes the agent
// before anything else, and its root waits for the agent's report before its
// first turn, so the root reads that report instead of answering nothing.
func (s *SubAgentResumeE2ESuite) TestFreshPortionResumeAlsoResumesDurableSiblings() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const chatID = "chat-resume-all-durable-siblings"
	input := spawnE2EWorkflowInput(chatID)
	input.ExecContext.ThreadMode = model.ThreadModeInherit
	requested := stoppedBefore(chatID, "requested", "tc-requested", 0)
	e.durableStopped["requested"] = requested
	e.durableStopped["sibling"] = stoppedBefore(chatID, "sibling", "tc-sibling", 1)
	input.Resume = &ResumeInput{AwaitSpawnsFirst: true, ResumeStopped: []SpawnHandoff{stoppedSpawnHandoff(requested)}, ResumeAllStopped: true}
	e.parentToolCalls[1] = func(string) []map[string]interface{} { return nil }
	e.mu.Lock()
	e.parentThread = input.ExecContext.Thread
	e.mu.Unlock()
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().NoError(env.GetWorkflowError())
	for _, thread := range []string{"requested", "sibling"} {
		turns, _ := e.threadTurns(thread)
		s.Equal(1, turns, "fresh portion resume restarts each unfinished sibling exactly once: %s", thread)
	}
}

func (s *SubAgentResumeE2ESuite) TestRecoveryOnlyRunsSetupBeforeLoopAndRecoversSibling() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const chatID = "recovery-setup-loop"
	input := spawnE2EWorkflowInput(chatID)
	input.Resume = &ResumeInput{RecoveryOnly: true, ResumeAllStopped: true}
	e.durableStopped["sibling"] = stoppedBefore(chatID, "sibling", "tc-sibling", 0)
	e.parentThread = input.ExecContext.Thread
	root := resumeTestWorkflow(s.T(), `
name: setup-loop
entry: [improve_prompt]
nodes:
  - id: improve_prompt
    type: call_llm
    args:
      thinking_level: setup
  - id: implementations
    type: loop
    while: false
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args:
            thinking_level: loop
edges:
  - from: improve_prompt
    default: implementations
`)
	child := resumeTestWorkflow(s.T(), subAgentResumeYAML)
	env.RegisterActivityWithOptions(func(_ context.Context, values types.LoadWorkflowInput) (LoadedWorkflow, error) {
		wf := root
		if values.WorkflowName == "builtin://agent" {
			wf = child
		}
		raw, err := protojson.Marshal(wf)
		return LoadedWorkflow{WorkflowJSON: raw}, err
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	var rootSteps []string
	env.RegisterActivityWithOptions(func(ctx context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		if in.Runtime.Thread != input.ExecContext.Thread {
			return e.callLLM(ctx, in)
		}
		e.mu.Lock()
		rootSteps = append(rootSteps, in.Runtime.StepID)
		e.mu.Unlock()
		return map[string]interface{}{"message": map[string]interface{}{"text": "prepared"}, "response_text": "prepared"}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	input.WorkflowName = "setup-loop"
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().NoError(env.GetWorkflowError())
	e.mu.Lock()
	s.Require().GreaterOrEqual(len(rootSteps), 2)
	s.Equal("improve_prompt", rootSteps[0], "setup must execute before the root loop")
	for _, step := range rootSteps[1:] {
		s.Equal("work", step, "only the loop may take additional turns to read the recovered sibling report")
	}
	e.mu.Unlock()
	turns, _ := e.threadTurns("sibling")
	s.Equal(1, turns)
}

func (s *SubAgentResumeE2ESuite) TestFreshRootMessageResumesDurableSiblings() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const chatID = "fresh-root-resumes-siblings"
	input := spawnE2EWorkflowInput(chatID)
	input.Resume = &ResumeInput{ResumeAllStopped: true}
	e.durableStopped["sibling"] = stoppedBefore(chatID, "sibling", "tc-sibling", 0)
	e.parentToolCalls[1] = func(string) []map[string]interface{} { return nil }
	e.mu.Lock()
	e.parentThread = input.ExecContext.Thread
	e.mu.Unlock()
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().NoError(env.GetWorkflowError())
	turns, _ := e.threadTurns("sibling")
	s.Equal(1, turns)
}

func (s *SubAgentResumeE2ESuite) TestRunStartedForAStoppedAgentResumesItBeforeTheRootTakesATurn() {
	env := s.NewTestWorkflowEnvironment()
	e := newSubAgentResumeEnv(s.T(), env, false)
	const chatID = "chat-run-started-for-stopped-agent"
	const stoppedAgent = "agent-stopped-before-this-run"
	const msg = "switched you to OpenAI, continue"

	input := spawnE2EWorkflowInput(chatID)
	input.ExecContext.ThreadMode = model.ThreadModeInherit
	handoff := stoppedSpawnHandoff(stoppedBefore(chatID, stoppedAgent, "tc-old", 2))
	input.Resume = &ResumeInput{AwaitSpawnsFirst: true, ResumeStopped: []SpawnHandoff{handoff}}
	// The message was queued before the run started.
	e.mailbox.enqueue(stoppedAgent, msg)
	// The root has nothing of its own to do.
	e.parentToolCalls[1] = func(string) []map[string]interface{} { return nil }

	e.mu.Lock()
	e.parentThread = input.ExecContext.Thread
	e.mu.Unlock()
	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	turns, read := e.threadTurns(stoppedAgent)
	s.Require().Equal(1, turns, "the stopped agent the run was started for must run")
	s.Require().Equal([]string{msg}, read[0])
	s.Require().Equal([]string{"2@tc-old#resume-3"}, e.reportsFromThread(stoppedAgent))

	rootTurns, rootRead := e.threadTurns(input.ExecContext.Thread)
	s.Require().Equal(1, rootTurns, "the root takes one turn, for the agent's report — not one before it")
	s.Require().Equal([]string{"report:tc-old#resume-3"}, rootRead[0],
		"the root's first turn is the one that reads what the resumed agent reported")
	s.Require().Empty(e.lookedUp(), "an agent handed to the run is not looked up again")
}

// stoppingFailureErr is a failure that really does stop a sub-agent: its
// provider rejects the request outright. (A provider usage limit does not — the
// agent waits it out in place; see usage_limit_wait_test.go.)
func stoppingFailureErr() error {
	return errors.New(`POST "https://api.example.com/v1/messages": 400 Bad Request invalid_request_error`)
}
