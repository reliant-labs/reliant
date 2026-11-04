// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Is a background spawn's report delivered when it lands after the parent's
// last look at its mailbox?
//
// CallLLM looks twice: it drains the thread's mailbox before reading history,
// and probes it again once the turn is over (pending_inbox), so a row that
// arrived during the turn buys one more. Neither look sees a report enqueued
// AFTER the probe. Nothing rings a doorbell for it either —
// EnqueueAgentMessage is called from the spawn's detached goroutine, inside
// the parent's own workflow — so the only thing left to deliver it is the
// loop-exit gate. The gate used to measure child progress from the moment it
// was reached; a child that had already finished and left the live set by
// then looked like no progress, and the run ended with the report queued.
//
// The spawn tests in spawn_background_e2e_test.go count turns. These model the
// mailbox the way production has it and assert on what was delivered.

// spawnReportUnreadYAML is spawnBackgroundE2EYAML plus the half of agent.yaml's
// while-condition this is about: the loop re-enters when call_llm's
// end-of-turn probe found something queued.
const spawnReportUnreadYAML = `
name: agent
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0) || outputs.pending_inbox == true
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
        pending_inbox: "{{has(nodes.call_llm) && has(nodes.call_llm.pending_inbox) ? nodes.call_llm.pending_inbox : false}}"
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

// fakeMailbox is agent_messages as far as these tests need it: one queue of
// undelivered reports per recipient thread, named by the spawn's tool call id.
type fakeMailbox struct {
	mu        sync.Mutex
	queued    map[string][]string
	delivered map[string][]string
}

func (m *fakeMailbox) enqueue(thread, report string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queued[thread] = append(m.queued[thread], report)
}

// drain is CallLLMActivity.drainAgentMailbox: everything queued for thread is
// delivered into its history.
func (m *fakeMailbox) drain(thread string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered[thread] = append(m.delivered[thread], m.queued[thread]...)
	delete(m.queued, thread)
}

// hasQueued is CallLLMActivity.hasQueuedAgentMessages, the probe behind
// pending_inbox.
func (m *fakeMailbox) hasQueued(thread string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queued[thread]) > 0
}

func (m *fakeMailbox) snapshot(thread string) (delivered, queued []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.delivered[thread]), slices.Clone(m.queued[thread])
}

// reportUnreadEnv is the spawn e2e env with the mailbox wired in: the
// background child's report is queued for the parent, and CallLLM drains and
// probes it where production does.
type reportUnreadEnv struct {
	*spawnE2EEnv
	mailbox *fakeMailbox

	// heldAfterProbe holds the parent's turn N, after its pending_inbox probe
	// and before it returns, until the named milestone.
	heldAfterProbe map[int]string
}

// milestoneParentTurnProbed is reached once the parent's turn has taken its
// end-of-turn look at the mailbox.
func milestoneParentTurnProbed(turn int) string {
	return fmt.Sprintf("parent-turn-%d-probed", turn)
}

func newReportUnreadEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment) *reportUnreadEnv {
	t.Helper()
	// Turn 1 spawns the child in the background; turn 2 has nothing to do,
	// which is the turn the loop-exit gate follows. Any later turn — the
	// child's one turn included — gets the script's exhausted "done".
	base := newSpawnE2EEnv(t, env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})
	e := &reportUnreadEnv{
		spawnE2EEnv: base,
		mailbox:     &fakeMailbox{queued: map[string][]string{}, delivered: map[string][]string{}},
	}

	// The test environment keeps the last registration for a name, so these
	// replace the base env's stubs.
	wf, err := wfyaml.ParseWorkflow([]byte(spawnReportUnreadYAML))
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
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			toThread, _ := input["to_thread_id"].(string)
			toolCallID, _ := input["tool_call_id"].(string)
			e.mailbox.enqueue(toThread, toolCallID)
			e.mu.Lock()
			e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneReportDelivered
			e.mu.Unlock()
			return map[string]interface{}{"id": "am-" + toolCallID}, nil
		},
		activity.RegisterOptions{Name: "EnqueueAgentMessage"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLM(ctx, input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	return e
}

// callLLM is the base stub's turn between the two looks CallLLMActivity takes
// at the mailbox.
func (e *reportUnreadEnv) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mailbox.drain(thread)

	// The turn itself, including any parentTurnWaitsFor hold: that hold sits
	// between the drain and the probe, the window pending_inbox covers.
	out, err := e.callLLMStub(ctx, input)
	if err != nil {
		return nil, err
	}
	out["pending_inbox"] = e.mailbox.hasQueued(thread)

	e.mu.Lock()
	isParent := thread == e.parentThread
	turn := e.callLLMByThread[thread]
	waitFor := ""
	if isParent {
		close(e.milestone(milestoneParentTurnProbed(turn)))
		waitFor = e.heldAfterProbe[turn]
	}
	e.mu.Unlock()
	if waitFor != "" {
		e.awaitMilestone(waitFor)
	}
	return out, nil
}

func (e *reportUnreadEnv) requireReportDelivered(t *testing.T) {
	t.Helper()
	delivered, queued := e.mailbox.snapshot(e.parentThread)
	require.Empty(t, queued,
		"the run ended with the child's report still queued in the parent's mailbox (parent took %d turns)", e.parentTurns())
	require.Equal(t, []string{"tc1"}, delivered,
		"the child's report must reach the parent exactly once")
}

type SpawnReportUnreadE2ESuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSpawnReportUnreadE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SpawnReportUnreadE2ESuite))
}

// The report lands after the parent's last turn has probed its mailbox, and
// the child has left the live set before the parent reaches the loop-exit
// gate. Neither look CallLLM takes sees it, pending_inbox is false, the turn
// has no tool calls, and nothing is live: only the gate, counting the child's
// completion from the start of the turn, can give the parent the turn that
// delivers it.
func (s *SpawnReportUnreadE2ESuite) TestReportLandingAfterTheLastProbeIsStillDelivered() {
	env := s.NewTestWorkflowEnvironment()
	e := newReportUnreadEnv(s.T(), env)
	e.childTurnWaitsFor = milestoneParentTurnProbed(2)
	e.heldAfterProbe = map[int]string{2: milestoneChildSettled}

	e.execute("chat-report-after-probe")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	e.requireReportDelivered(s.T())
}

// The control: the report lands during the parent's last turn, after its
// drain but before its probe. pending_inbox catches it and re-enters the loop
// — the gate is not involved — so this is delivered with or without the gate
// measuring from the turn's start.
func (s *SpawnReportUnreadE2ESuite) TestReportLandingBeforeTheLastProbeRidesPendingInbox() {
	env := s.NewTestWorkflowEnvironment()
	e := newReportUnreadEnv(s.T(), env)
	e.childTurnWaitsFor = milestoneParentTurnStarted(2)
	e.parentTurnWaitsFor = map[int]string{2: milestoneChildSettled}

	e.execute("chat-report-before-probe")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	e.requireReportDelivered(s.T())
}

// The report is in the mailbox BEFORE the parent's turn begins, and the child
// is only counted complete during that turn — the order a real worker
// produces, because the spawn's terminal child and tool-call statuses come
// after its report. The turn drains the report and answers it with no tool
// calls; nothing is live at the gate.
//
// The gate must exit. Counting the completion alone, it re-entered for a
// report that turn had just delivered: the extra turn drained nothing, found
// history ending with the assistant, and logged "History ends with an
// assistant message after the mailbox drain" at ERROR. Chat 9fbaace8 (Sentry
// ELECTRON-CM) in production: the parent's blocking spawn_status returned in
// the same workflow task as the child's report, and the child's tool-call
// status landed 230ms into the parent's next turn.
func (s *SpawnReportUnreadE2ESuite) TestReportDeliveredBeforeTheChildSettlesEarnsNoExtraTurn() {
	env := s.NewTestWorkflowEnvironment()
	e := newReportUnreadEnv(s.T(), env)
	// Turn 1 spawns; turn 2 calls a tool that, like spawn_status waiting on
	// its agent, returns only once the report has landed. The child's turn is
	// the script's exhausted "done".
	e.script = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: []map[string]interface{}{{"id": "tc2", "name": "shell", "input": `{"command":"true"}`}}},
	}
	e.childTurnWaitsFor = milestoneParentTurnDelivered(2)
	e.toolsWaitFor = milestoneReportDelivered
	// The child settles only once turn 3 is under way, and turn 3 does not
	// return until it has, so nothing is live when the parent reaches the gate.
	e.childSettleWaitsFor = milestoneParentTurnStarted(3)
	e.parentTurnWaitsFor = map[int]string{3: milestoneChildSettled}

	e.execute("chat-report-before-turn")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	e.requireReportDelivered(s.T())
	s.Require().Equal(3, e.parentTurns(),
		"turn 3 delivered the report; a turn 4 would have nothing to answer")
}
