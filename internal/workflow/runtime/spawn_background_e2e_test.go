// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"google.golang.org/protobuf/encoding/protojson"
)

// End-to-end (full DynamicWorkflow) coverage for spec §11 items 1, 2, 6:
// lifetime and no-spin, exercised through the real call_llm → execute_tools
// → loop wiring rather than by calling awaitLiveDetachedSpawns directly
// (spawn_background_lifetime_test.go covers that in isolation).

// spawnBackgroundE2EYAML is agent.yaml's shape trimmed to what these tests
// need: a loop around call_llm → execute_tools, no approval/compaction/
// ask_question branches.
const spawnBackgroundE2EYAML = `
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

// scriptedToolCallsResponse is one entry in a scripted CallLLM sequence: the
// tool_calls (as raw JSON tool-call objects) to return on that turn. An empty
// slice means "no tool calls" — the turn that would normally end the loop.
type scriptedToolCallsResponse struct {
	toolCalls []map[string]interface{}
}

// spawnE2EEnv wires DynamicWorkflow's full activity surface for these tests
// and records what happened.
type spawnE2EEnv struct {
	t   *testing.T
	env *testsuite.TestWorkflowEnvironment

	mu            sync.Mutex
	callLLMCount  int32
	toolResultsBy map[string][]interface{} // turn index -> tool_results seen by execute_tools
	statuses      []map[string]interface{}
	toolStatuses  []map[string]interface{}
	// events is the shared activity order for spawn report/terminal-status
	// ordering assertions: "report:<tc>", "child-status:<tc>:<status>",
	// "tool-status:<tc>:<status>".
	events []string

	// script is indexed by callLLMCount (the global CallLLM sequence number
	// across the parent and any spawned child), so the turn a stub serves is
	// determined by that counter rather than by a separate cursor.
	script []scriptedToolCallsResponse

	// callLLMRuntimes is the RuntimeContext every CallLLM was handed, parent
	// and spawned children alike, in call order.
	callLLMRuntimes []types.RuntimeContext

	// callLLMByThread counts CallLLM turns per thread, so a test can tell the
	// parent's turns from the background child's.
	callLLMByThread map[string]int

	// parentThread is the thread DynamicWorkflow runs the parent loop on. Set
	// by execute; empty means "unknown" and disables the holds below.
	parentThread string

	// Interleaving holds. The test environment runs every activity on its own
	// goroutine, so whether the background child finishes before, during or
	// after the parent's turn is otherwise decided by the Go scheduler — and
	// the parent loop's behavior legitimately differs between those cases.
	// A test that depends on one of them pins it here instead of hoping.
	//
	// childTurnWaitsFor holds the child's CallLLM until the named milestone;
	// parentTurnWaitsFor holds the parent's turn N CallLLM likewise.
	childTurnWaitsFor  string
	parentTurnWaitsFor map[int]string

	// wakeDuringParentTurn > 0 rings the parent thread's wake doorbell (a
	// user message arriving) while the parent's turn N is in flight. The
	// signal is posted before that turn's result, so the workflow sees the
	// wake first.
	wakeDuringParentTurn int

	// milestones are reached in one of two ways. "started" milestones are
	// closed by the stub itself, on entry. The rest are closed by the env's
	// OnActivityCompleted listener, which runs on the test dispatcher after an
	// activity's result is handed to the workflow and BEFORE the workflow
	// task that reacts to it — so a stub waiting on one resumes, and its own
	// result is delivered, only after the workflow has fully acted on that
	// activity.
	milestones  map[string]chan struct{}
	milestoneOf map[string]string // activity ID -> milestone its delivery reaches
	testDone    chan struct{}     // closed by t.Cleanup
}

// milestoneChildSettled is reached once the spawn's terminal tool status has
// been delivered: its detached goroutine has finished and left the live
// registry.
const milestoneChildSettled = "child-settled"

func milestoneParentTurnStarted(turn int) string {
	return fmt.Sprintf("parent-turn-%d-started", turn)
}

func milestoneParentTurnDelivered(turn int) string {
	return fmt.Sprintf("parent-turn-%d-delivered", turn)
}

// milestone returns the channel for name, creating it on first use. Callers
// hold e.mu.
func (e *spawnE2EEnv) milestone(name string) chan struct{} {
	ch, ok := e.milestones[name]
	if !ok {
		ch = make(chan struct{})
		e.milestones[name] = ch
	}
	return ch
}

// awaitMilestone blocks an activity stub until name is reached. A workflow
// that never gets there stalls, and the test environment fails the test on
// its own (its 3s no-progress timeout, with the workflow's stack); the stub is
// then released by the test's cleanup rather than left blocked behind it.
func (e *spawnE2EEnv) awaitMilestone(name string) {
	e.mu.Lock()
	ch := e.milestone(name)
	e.mu.Unlock()
	select {
	case <-ch:
	case <-e.testDone:
	}
}

// execute runs DynamicWorkflow for chatID, recording the parent's thread so
// per-thread turn counts and the interleaving holds can find it.
func (e *spawnE2EEnv) execute(chatID string) {
	input := spawnE2EWorkflowInput(chatID)
	e.mu.Lock()
	e.parentThread = input.ExecContext.Thread
	e.mu.Unlock()
	e.env.ExecuteWorkflow(DynamicWorkflow, input)
}

func newSpawnE2EEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, script []scriptedToolCallsResponse) *spawnE2EEnv {
	t.Helper()
	e := &spawnE2EEnv{
		t:               t,
		env:             env,
		script:          script,
		toolResultsBy:   map[string][]interface{}{},
		callLLMByThread: map[string]int{},
		milestones:      map[string]chan struct{}{},
		milestoneOf:     map[string]string{},
		testDone:        make(chan struct{}),
	}
	t.Cleanup(func() { close(e.testDone) })

	env.SetOnActivityCompletedListener(func(info *activity.Info, _ converter.EncodedValue, err error) {
		if err != nil {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if name, ok := e.milestoneOf[info.ActivityID]; ok {
			delete(e.milestoneOf, info.ActivityID)
			close(e.milestone(name))
		}
	})

	wf, err := wfyaml.ParseWorkflow([]byte(spawnBackgroundE2EYAML))
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
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.statuses = append(e.statuses, input)
			if tc, _ := input["spawned_by_tool_call_id"].(string); tc != "" {
				e.events = append(e.events, fmt.Sprintf("child-status:%s:%v", tc, input["status"]))
			}
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.toolStatuses = append(e.toolStatuses, input)
			e.events = append(e.events, fmt.Sprintf("tool-status:%v:%v", input["tool_call_id"], input["status"]))
			// The spawn's terminal status is the last thing its detached
			// goroutine waits on before leaving the live registry.
			if input["tool_call_id"] == "tc1" && input["status"] == "completed" {
				e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneChildSettled
			}
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitToolCallStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		},
		activity.RegisterOptions{Name: "EmitThreadEvent"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"message_id": "msg-" + input["thread_id"].(string)}, nil
		},
		activity.RegisterOptions{Name: "CreateWorkflowWithThread"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"message_id": "msg-inject"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			toolCallID, _ := input["tool_call_id"].(string)
			return map[string]interface{}{
				"content":  "spawned agent finished: found the answer",
				"is_error": false,
				"_synth":   toolCallID,
			}, nil
		},
		activity.RegisterOptions{Name: "FetchThreadResult"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"count": 0, "has_messages": false}, nil
		},
		activity.RegisterOptions{Name: "DrainAgentMessages"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.events = append(e.events, fmt.Sprintf("report:%v", input["tool_call_id"]))
			e.mu.Unlock()
			return map[string]interface{}{"id": "am-" + input["tool_call_id"].(string)}, nil
		},
		activity.RegisterOptions{Name: "EnqueueAgentMessage"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"valid": true}, nil
		},
		activity.RegisterOptions{Name: "ValidateThreadOwnership"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "LoadPresetParams"},
	)

	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.executeToolsStub(input)
		},
		activity.RegisterOptions{Name: "ExecuteTools"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLMStub(ctx, input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	return e
}

func (e *spawnE2EEnv) callLLMStub(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mu.Lock()
	e.callLLMByThread[thread]++
	turn := e.callLLMByThread[thread]
	isParent := e.parentThread != "" && thread == e.parentThread
	var waitFor string
	wake := false
	if isParent {
		close(e.milestone(milestoneParentTurnStarted(turn)))
		e.milestoneOf[activity.GetInfo(ctx).ActivityID] = milestoneParentTurnDelivered(turn)
		waitFor = e.parentTurnWaitsFor[turn]
		wake = e.wakeDuringParentTurn == turn
	} else if e.parentThread != "" {
		waitFor = e.childTurnWaitsFor
	}
	e.mu.Unlock()

	if waitFor != "" {
		e.awaitMilestone(waitFor)
	}
	if wake {
		e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonUserMessage})
	}

	idx := int(atomic.AddInt32(&e.callLLMCount, 1)) - 1
	e.mu.Lock()
	defer e.mu.Unlock()
	e.callLLMRuntimes = append(e.callLLMRuntimes, input.Runtime)
	if idx >= len(e.script) {
		// Script exhausted: no tool calls, loop ends.
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	resp := e.script[idx]
	toolCallsJSON, err := json.Marshal(resp.toolCalls)
	require.NoError(e.t, err)
	var toolCalls []interface{}
	require.NoError(e.t, json.Unmarshal(toolCallsJSON, &toolCalls))
	return map[string]interface{}{
		"response_text": "working",
		"tool_calls":    toolCalls,
	}, nil
}

// executeToolsStub runs the SAME split/dispatch logic executeToolsWithSpawnSupport
// wraps, by invoking it directly against a stub "regular tool" path — but for
// these tests all tool calls are spawn calls, so this stub only has to satisfy
// the ExecuteTools activity contract for the regular (non-spawn) split, which
// spawnBackgroundE2EYAML's scripts never populate.
func (e *spawnE2EEnv) executeToolsStub(input types.ActivityInput) (map[string]interface{}, error) {
	// Every tool call in these tests is "spawn" and is intercepted before
	// reaching the ExecuteTools activity (splitProtoToolCalls in
	// executeToolsWithSpawnSupport). If this stub is ever invoked, it means
	// a non-spawn regular tool call reached here — return an empty result.
	return map[string]interface{}{"tool_results": []interface{}{}}, nil
}

func (e *spawnE2EEnv) callLLMInvocations() int {
	return int(atomic.LoadInt32(&e.callLLMCount))
}

// parentTurns is how many CallLLM turns the parent loop took.
func (e *spawnE2EEnv) parentTurns() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callLLMByThread[e.parentThread]
}

func spawnToolCall(id, prompt string) map[string]interface{} {
	input := map[string]interface{}{"preset": "general", "prompt": prompt}
	inputJSON, _ := json.Marshal(input)
	return map[string]interface{}{
		"id":    id,
		"name":  "spawn",
		"input": string(inputJSON),
	}
}

func spawnE2EWorkflowInput(chatID string) WorkflowInput {
	return WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "agent",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-" + chatID,
			ChatID:       chatID,
			Thread:       "thread-" + chatID,
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "agent",
			ProjectPath:  "/project",
		},
	}
}

type SpawnBackgroundE2ESuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSpawnBackgroundE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SpawnBackgroundE2ESuite))
}

// TestBackground_LoopWaitsForDetachedSpawnThenDelivers is the regression
// that matters most (spec §11 item 1): the LLM emits ONE background spawn
// call, then (turn 2) no tool calls at all. The loop must NOT exit — it must
// block for the detached spawn, then react to its mailbox-delivered result
// on a THIRD call_llm turn.
func (s *SpawnBackgroundE2ESuite) TestBackground_LoopWaitsForDetachedSpawnThenDelivers() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil}, // turn 2: no tool calls — this is where a naive impl would exit
	})

	// The child's one turn is held until the parent's turn 2 has been
	// delivered, so the child is still live when the parent reaches the
	// loop-exit gate and the parent genuinely has to wait for it.
	e.childTurnWaitsFor = milestoneParentTurnDelivered(2)

	e.execute("chat-bg-lifetime")

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	// The parent must have taken a THIRD call_llm turn: turn 1 (spawns),
	// turn 2 (no tool calls — the exit candidate), turn 3 (after the
	// detached spawn's mailbox completion was drained and delivered).
	// Counted per thread: the child's own turn must not stand in for it.
	require.Equal(s.T(), 3, e.parentTurns(),
		"the loop must not exit at turn 2; it must wait for the detached spawn and react to its result")

	// The background spawn's tool call must have gone through "backgrounded"
	// status (not the sync "executing"->"completed" pair).
	sawBackgrounded := false
	for _, st := range e.toolStatuses {
		if st["tool_call_id"] == "tc1" && st["status"] == "backgrounded" {
			sawBackgrounded = true
		}
	}
	require.True(s.T(), sawBackgrounded, "a background spawn must record status=backgrounded, not executing/completed")
}

// TestBackground_NoSpin asserts spec §11 item 2: while the loop is blocked
// waiting for a detached spawn, no EXTRA call_llm turns are burned just
// re-checking "still waiting" — workflow.Await parks the goroutine, it does
// not re-enter the step machinery.
//
// This is checked by bounding the total call_llm count for the scenario
// above: exactly 3 (parent turn 1, parent turn 2 the exit candidate, parent
// turn 3 after delivery) plus 1 for the spawned child's own single turn = 4.
// Any polling implementation would burn additional turns proportional to
// however long the detached goroutine took to finish.
//
// The child is held until the parent's turn 2 has been delivered, so the
// parent is parked at the gate while the child runs — the situation the
// no-spin rule is about. Left to the scheduler, the child could instead
// finish during the parent's turn 2, which is a different case (see
// TestBackground_SpawnFinishingMidTurnStillGetsATurn).
func (s *SpawnBackgroundE2ESuite) TestBackground_NoSpin() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})
	e.childTurnWaitsFor = milestoneParentTurnDelivered(2)

	e.execute("chat-bg-nospin")

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	require.Equal(s.T(), 4, e.callLLMInvocations(),
		"exactly parent-turn-1 + parent-turn-2 + parent-turn-3 + child's-one-turn — no extra turns from polling while blocked")
	require.Equal(s.T(), 3, e.parentTurns(), "the parent takes exactly three turns")
}

// TestBackground_SpawnFinishingMidTurnStillGetsATurn pins the interleaving
// the loop-exit gate used to lose: the background child finishes WHILE the
// parent's last turn is still in flight, so by the time the parent reaches
// the gate there is nothing live to wait on.
//
// That turn read its mailbox before the child reported, so the report is
// still undelivered. The gate must re-enter once more to deliver it rather
// than exit on "nothing live". It used to snapshot the completion count at
// the gate itself — after the completion had already happened — so it saw
// no progress and exited, and the parent finished without ever reacting to
// its agent's result. This was TestBackground_NoSpin's flake (3 turns
// instead of 4) whenever the scheduler happened to run the child first.
func (s *SpawnBackgroundE2ESuite) TestBackground_SpawnFinishingMidTurnStillGetsATurn() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})
	// The child runs only once the parent's turn 2 is under way, and that
	// turn does not return until the child has finished and left the live
	// registry. Both halves matter: a child that finished BEFORE turn 2
	// began would have had its report drained by turn 2 itself, and then
	// exiting after turn 2 is the right answer.
	e.childTurnWaitsFor = milestoneParentTurnStarted(2)
	e.parentTurnWaitsFor = map[int]string{2: milestoneChildSettled}

	e.execute("chat-bg-midturn")

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	require.Equal(s.T(), 3, e.parentTurns(),
		"a spawn that finished during turn 2 must still earn the parent a turn 3 to deliver its result")
	require.Equal(s.T(), 4, e.callLLMInvocations(),
		"and exactly one: three parent turns plus the child's one")
}

// TestBackground_WakeDuringTurnIsNotMissed is the same lost wakeup for the
// doorbell: a user message lands on the parent's thread while its last turn
// is in flight and a background child is still running. That turn read
// history before the message arrived, so the gate must re-enter for it at
// once. Measuring from the gate, it saw no new wake and parked until the
// child happened to finish — the "blocked on its sub-agent" chat that the
// doorbell was added to fix, still reachable through this window.
func (s *SpawnBackgroundE2ESuite) TestBackground_WakeDuringTurnIsNotMissed() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})
	// The doorbell rings during turn 2, and the child is held until the
	// parent has started turn 3, so only the wake can produce turn 3. A
	// parent that parks instead waits on a child that is waiting on it.
	e.wakeDuringParentTurn = 2
	e.childTurnWaitsFor = milestoneParentTurnStarted(3)

	e.execute("chat-bg-wake")

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	require.Equal(s.T(), 4, e.parentTurns(),
		"turn 1 spawns, turn 2 is in flight when the message lands, turn 3 answers it, turn 4 reacts to the child's report")
}
