// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Continue-as-new with live background spawns (specs/continue-as-new-with-spawns.md).
//
// Chat 0e15fdba died because a run past the handoff threshold REFUSED to hand
// off while any background spawn was live: six sub-agents kept the history
// growing from 40 MB to 52 MB while the main thread was parked waiting on
// them, and Temporal terminated it. These tests pin the replacement: spawns
// park at their own iteration boundary, cross the boundary in
// ResumeInput.Spawns, and are relaunched on the same thread and tool call.

// canSpawnEnv is a full-DynamicWorkflow harness whose CallLLM is scripted
// PER THREAD, so the parent and each spawned child advance independently.
type canSpawnEnv struct {
	t   *testing.T
	env *testsuite.TestWorkflowEnvironment

	mu sync.Mutex
	// scripts[thread] is consumed one entry per CallLLM on that thread; an
	// exhausted script returns "no tool calls".
	scripts map[string][]scriptedToolCallsResponse
	// llmCalls[thread] counts CallLLM invocations per thread.
	llmCalls map[string]int
	// llmIterations[thread] records the loop iteration each call ran at.
	llmIterations map[string][]int
	// onLLM, when set, runs inside CallLLM (after counting) — used to flip
	// the pressure seam at a precise point in a thread's progress.
	onLLM func(thread string, n int)

	enqueued     []map[string]interface{}
	toolStatuses []map[string]interface{}
	createdRows  []string
}

func newCANSpawnEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment) *canSpawnEnv {
	t.Helper()
	e := &canSpawnEnv{
		t: t, env: env,
		scripts:       map[string][]scriptedToolCallsResponse{},
		llmCalls:      map[string]int{},
		llmIterations: map[string][]int{},
	}
	wf, err := wfyaml.ParseWorkflow([]byte(spawnBackgroundE2EYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	ok := func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "Cleanup", "EmitThreadEvent", "WorkflowError", "ValidateThreadOwnership", "LoadPresetParams", "SaveMessage"} {
		name := name
		switch name {
		case "ValidateThreadOwnership":
			env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"valid": true}, nil
			}, activity.RegisterOptions{Name: name})
		case "LoadPresetParams":
			env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{}, nil
			}, activity.RegisterOptions{Name: name})
		case "SaveMessage":
			env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"message_id": "msg-inject"}, nil
			}, activity.RegisterOptions{Name: name})
		default:
			env.RegisterActivityWithOptions(ok, activity.RegisterOptions{Name: name})
		}
	}
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.createdRows = append(e.createdRows, input["thread_id"].(string))
		e.mu.Unlock()
		return map[string]interface{}{"message_id": "msg-" + input["thread_id"].(string)}, nil
	}, activity.RegisterOptions{Name: "CreateWorkflowWithThread"})
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.toolStatuses = append(e.toolStatuses, input)
		e.mu.Unlock()
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "EmitToolCallStatus"})
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"content": "child result", "is_error": false}, nil
	}, activity.RegisterOptions{Name: "FetchThreadResult"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"count": 0, "has_messages": false}, nil
	}, activity.RegisterOptions{Name: "DrainAgentMessages"})
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.enqueued = append(e.enqueued, input)
		e.mu.Unlock()
		return map[string]interface{}{"id": "am-" + input["tool_call_id"].(string)}, nil
	}, activity.RegisterOptions{Name: "EnqueueAgentMessage"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
		return map[string]interface{}{"tool_results": []interface{}{}}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	env.RegisterActivityWithOptions(func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		return e.callLLM(input)
	}, activity.RegisterOptions{Name: "CallLLM"})
	return e
}

func (e *canSpawnEnv) callLLM(input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mu.Lock()
	e.llmCalls[thread]++
	n := e.llmCalls[thread]
	e.llmIterations[thread] = append(e.llmIterations[thread], input.Runtime.LoopIteration)
	var resp scriptedToolCallsResponse
	if script := e.scripts[thread]; len(script) > 0 {
		resp = script[0]
		e.scripts[thread] = script[1:]
	}
	hook := e.onLLM
	e.mu.Unlock()
	if hook != nil {
		hook(thread, n)
	}
	if len(resp.toolCalls) == 0 {
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}
	raw, err := json.Marshal(resp.toolCalls)
	require.NoError(e.t, err)
	var toolCalls []interface{}
	require.NoError(e.t, json.Unmarshal(raw, &toolCalls))
	return map[string]interface{}{"response_text": "working", "tool_calls": toolCalls}, nil
}

// noopToolCall is a regular (non-spawn) tool call: one iteration of real work.
func noopToolCall(id string) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": "view", "input": `{"path":"x"}`}
}

func repeatTurns(prefix string, n int) []scriptedToolCallsResponse {
	out := make([]scriptedToolCallsResponse, n)
	for i := range out {
		out[i] = scriptedToolCallsResponse{toolCalls: []map[string]interface{}{noopToolCall(prefix + string(rune('a'+i)))}}
	}
	return out
}

// forcePressure installs the history-pressure test seam for one chat.
func forcePressure(t *testing.T, chatID string, fn func() historyPressure) {
	t.Helper()
	historyPressureOverrides.Store(chatID, fn)
	t.Cleanup(func() { historyPressureOverrides.Delete(chatID) })
}

// carriedInput decodes the continuation a finished run handed off, failing
// the test if the run did not continue as new.
func carriedInput(t *testing.T, env *testsuite.TestWorkflowEnvironment) WorkflowInput {
	t.Helper()
	require.True(t, env.IsWorkflowCompleted())
	var contErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &contErr),
		"expected a ContinueAsNewError, got %v", env.GetWorkflowError())
	var carried WorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &carried))
	return carried
}

const canParentWorkflowID = "default-test-workflow-id"

func canChildThread(toolCallID string) string {
	return DeterministicWorkflowID(canParentWorkflowID, toolCallID)
}

func (e *canSpawnEnv) toolStatusesFor(toolCallID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, st := range e.toolStatuses {
		if st["tool_call_id"] == toolCallID {
			s, _ := st["status"].(string)
			out = append(out, s)
		}
	}
	return out
}

func (e *canSpawnEnv) enqueuedFor(toolCallID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, m := range e.enqueued {
		if m["tool_call_id"] == toolCallID {
			n++
		}
	}
	return n
}

// ── CAN with a live spawn mid-loop ────────────────────────────────────────────

// The parent spawns, then keeps working; the spawn is mid-loop when history
// crosses the threshold. The spawn parks at its next boundary, the parent
// hands off with ONE SpawnHandoff, and the successor relaunches it on the same
// thread and tool call: the spawn finishes there, the parent receives exactly
// one completion, and the tool call ends completed — never stranded at
// backgrounded.
func TestContinueAsNew_CarriesLiveSpawnMidLoop(t *testing.T) {
	t.Parallel()
	const chatID = "chat-can-live-spawn"
	child := canChildThread("tc-spawn")

	var suite1 testsuite.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	parent := "thread-" + chatID
	e1.scripts[parent] = append([]scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-spawn", "long research")}},
	}, repeatTurns("p", 10)...)
	e1.scripts[child] = repeatTurns("c", 10)

	pressure := historyPressureNone
	forcePressure(t, chatID, func() historyPressure { return pressure })
	e1.onLLM = func(thread string, n int) {
		// The spawn is two iterations in: history crosses the threshold.
		if thread == child && n == 2 {
			pressure = historyPressureSoft
		}
	}

	env1.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	carried := carriedInput(t, env1)

	require.NotNil(t, carried.Resume)
	require.Len(t, carried.Resume.Spawns, 1, "the live spawn must cross the boundary")
	handoff := carried.Resume.Spawns[0]
	require.Equal(t, "tc-spawn", handoff.ToolCallID)
	require.Equal(t, child, handoff.ChildThread)
	require.Equal(t, parent, handoff.ParentThread)
	require.Equal(t, 2, handoff.LoopIteration, "parked at the boundary after its second iteration")
	require.Zero(t, e1.enqueuedFor("tc-spawn"), "a parked spawn reports nothing in the predecessor")
	require.NotContains(t, e1.toolStatusesFor("tc-spawn"), "completed")
	require.NotContains(t, e1.toolStatusesFor("tc-spawn"), "cancelled")
	require.NotContains(t, e1.toolStatusesFor("tc-spawn"), "failed")

	// Successor: history is fresh; the relaunched spawn runs out its script.
	pressure = historyPressureNone
	var suite2 testsuite.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	e2 := newCANSpawnEnv(t, env2)
	e2.scripts[child] = repeatTurns("r", 2)

	env2.ExecuteWorkflow(DynamicWorkflow, carried)
	require.True(t, env2.IsWorkflowCompleted())
	require.NoError(t, env2.GetWorkflowError())

	require.NotContains(t, e2.createdRows, child, "the relaunch reuses the existing thread; no new thread or seed message")
	require.Equal(t, 2, e2.llmIterations[child][0], "the relaunched spawn resumes at the iteration it parked at")
	require.Equal(t, 1, e2.enqueuedFor("tc-spawn"), "the parent receives exactly one completion")
	require.Contains(t, e2.toolStatusesFor("tc-spawn"), "completed", "the tool call ends completed, not stranded at backgrounded")
}

// ── Main thread parked in awaitLiveDetachedSpawns ─────────────────────────────

// The main thread has finished its turn and is PARKED waiting on its spawn
// when the threshold is crossed — the exact shape of chat 0e15fdba, where no
// check ran for the entire wait. The handoff must still happen, and the
// successor must wait on the relaunched spawn instead of spending a turn.
func TestContinueAsNew_MainThreadParkedOnSpawns(t *testing.T) {
	t.Parallel()
	const chatID = "chat-can-parked-main"
	child := canChildThread("tc-spawn")
	parent := "thread-" + chatID

	var suite1 testsuite.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	e1.scripts[parent] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-spawn", "long research")}},
		{toolCalls: nil}, // parent parks in awaitLiveDetachedSpawns
	}
	e1.scripts[child] = repeatTurns("c", 10)

	pressure := historyPressureNone
	forcePressure(t, chatID, func() historyPressure { return pressure })
	e1.onLLM = func(thread string, n int) {
		if thread == child && n == 3 {
			pressure = historyPressureSoft
		}
	}

	env1.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	carried := carriedInput(t, env1)
	require.Equal(t, 2, e1.llmCalls[parent], "the parent was parked, not taking turns")
	require.True(t, carried.Resume.AwaitSpawnsFirst, "the successor must re-park on the spawn")
	require.Len(t, carried.Resume.Spawns, 1)

	pressure = historyPressureNone
	var suite2 testsuite.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	e2 := newCANSpawnEnv(t, env2)
	e2.scripts[child] = repeatTurns("r", 1)
	var parentCallsBeforeChildDone int
	e2.onLLM = func(thread string, n int) {
		if thread == child && n == 2 { // the child's final (no-tool-calls) turn
			parentCallsBeforeChildDone = e2.llmCalls[parent]
		}
	}

	env2.ExecuteWorkflow(DynamicWorkflow, carried)
	require.NoError(t, env2.GetWorkflowError())
	require.Zero(t, parentCallsBeforeChildDone, "the resumed parent waits on the relaunched spawn before its first turn")
	require.Equal(t, 1, e2.enqueuedFor("tc-spawn"))
}

// ── Hard backstop ─────────────────────────────────────────────────────────────

// Past the hard threshold the handoff stops waiting: a spawn that has not
// reached a boundary is carried anyway, from its last recorded boundary. Below
// it, an unparked spawn holds the handoff.
//
// Driven at the decision level rather than end to end: making a test-env spawn
// be reliably mid-activity at the instant its parent reaches a boundary is not
// deterministic, and the successor half of carrying a spawn is exactly what
// TestContinueAsNew_CarriesLiveSpawnMidLoop already runs end to end.
func TestContinueAsNew_HardBackstopCarriesUnparkedSpawn(t *testing.T) {
	t.Parallel()
	const chatID = "chat-can-hard"
	pressure := historyPressureSoft
	forcePressure(t, chatID, func() historyPressure { return pressure })

	tracker := &ChildWorkflowTracker{handoffCapable: 1, chatID: chatID}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc-spawn", ChatID: chatID, ParentThread: "p", ChildThread: "child",
		// Its loop last recorded the boundary of iteration 1 and is now
		// mid-iteration: never parked.
		handoff: SpawnHandoff{ToolCallID: "tc-spawn", ParentThread: "p", ChildThread: "child", LoopIteration: 1},
	})

	type decision struct {
		SoftReady bool
		HardReady bool
		Carried   []SpawnHandoff
	}
	wf := func(ctx workflow.Context) (decision, error) {
		var d decision
		d.SoftReady = readyToContinueAsNew(ctx, tracker, false)
		pressure = historyPressureHard
		d.HardReady = readyToContinueAsNew(ctx, tracker, false)
		err := newContinueAsNewError(ctx, WorkflowInput{ChatID: chatID, ExecContext: &ExecutionContext{}}, "agent_loop", 9, tracker, false)
		var contErr *workflow.ContinueAsNewError
		if errors.As(err, &contErr) {
			var carried WorkflowInput
			_ = converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &carried)
			d.Carried = carried.Resume.Spawns
		}
		return d, nil
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(wf)
	env.ExecuteWorkflow(wf)
	var d decision
	require.NoError(t, env.GetWorkflowResult(&d))

	require.False(t, d.SoftReady, "below the hard threshold an unparked spawn holds the handoff")
	require.True(t, d.HardReady, "past the hard threshold the handoff stops waiting")
	require.Len(t, d.Carried, 1, "the unparked spawn is carried anyway")
	require.Equal(t, 1, d.Carried[0].LoopIteration, "from its last recorded boundary")
}

// ── Pause armed ───────────────────────────────────────────────────────────────

// An armed pause blocks the handoff even with no spawns: a continuation would
// drop the resume signal and come back running.
func TestContinueAsNew_PauseArmedBlocks(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{handoffCapable: 1, chatID: "chat-can-pause"}
	forcePressure(t, "chat-can-pause", func() historyPressure { return historyPressureHard })
	require.False(t, quiescentForContinueAsNew(tracker, true))

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(func(ctx workflow.Context) (bool, error) {
		return readyToContinueAsNew(ctx, tracker, true), nil
	})
	env.ExecuteWorkflow(func(ctx workflow.Context) (bool, error) {
		return readyToContinueAsNew(ctx, tracker, true), nil
	})
	var ready bool
	require.NoError(t, env.GetWorkflowResult(&ready))
	require.False(t, ready, "an armed pause must block the handoff even past the hard threshold")
}

// ── Cancel while parked ───────────────────────────────────────────────────────

// A cancel_thread that reaches a spawn AFTER it parked must survive the
// boundary: the successor stops the relaunched spawn at its first boundary and
// reports it cancelled, rather than running it on.
func TestContinueAsNew_CancelWhileParkedHonoredInSuccessor(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc-spawn", ChatID: "c", ParentThread: "p", ChildThread: "child",
		handoff: SpawnHandoff{ToolCallID: "tc-spawn", ParentThread: "p", ChildThread: "child", LoopIteration: 4},
		parked:  true,
	})
	cancelled := map[string]bool{"tc-spawn": true}
	tracker.spawnCancelled = func(toolCallID, childThread string) bool {
		return cancelled[toolCallID] || cancelled[childThread]
	}
	handoffs := tracker.spawnHandoffs()
	require.Len(t, handoffs, 1)
	require.True(t, handoffs[0].Cancelled, "a cancel recorded after parking crosses the boundary")

	// Successor side, end to end.
	const chatID = "chat-can-cancel"
	child := canChildThread("tc-spawn")
	parent := "thread-" + chatID
	input := spawnE2EWorkflowInput(chatID)
	input.Resume = &ResumeInput{
		NodeID:           "agent_loop",
		LoopIteration:    3,
		AwaitSpawnsFirst: true,
		Spawns: []SpawnHandoff{{
			ToolCallID: "tc-spawn", ParentThread: parent, ChildThread: child,
			ChildWorkflowID: child, ParentWorkflowID: canParentWorkflowID,
			Preset: "general", LoopIteration: 4, Cancelled: true,
		}},
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	e := newCANSpawnEnv(t, env)
	e.scripts[child] = repeatTurns("r", 5)

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	require.Zero(t, e.llmCalls[child], "a spawn cancelled while parked must not take another turn")
	require.Contains(t, e.toolStatusesFor("tc-spawn"), "cancelled")
	require.Equal(t, 1, e.enqueuedFor("tc-spawn"), "the parent is told once that it was cancelled")
}

// A spawn relaunched by a fresh restart has no ChildInputs (it is derived from
// durable rows); its execution context must still be complete.
func TestPrepareSpawnRelaunch_DerivesMissingFields(t *testing.T) {
	t.Parallel()
	prep := prepareSpawnRelaunch(
		&spawnChildWorkflowConfig{toolCallID: "tc", childWorkflowID: "cw", childThread: "ct", presetName: "general", isResumption: true},
		SpawnHandoff{ToolCallID: "tc", ParentThread: "pt", ChildThread: "ct", ParentWorkflowID: "pw"},
		"chat", "/project", map[string]interface{}{"mode": "auto"},
		func(string) *PauseController { return &PauseController{} },
	)
	require.Equal(t, "ct", prep.childExecContext.Thread)
	require.Equal(t, model.ThreadModeInherit, prep.childExecContext.ThreadMode)
	require.Equal(t, 1, prep.childExecContext.SpawnDepth)
	require.Equal(t, "mutating", prep.childExecContext.ParentPermission)
	require.Equal(t, spawnNodeID("tc"), prep.spawnNode.GetId())
}

type testsuiteHolder struct{ testsuite.WorkflowTestSuite }

func (h *testsuiteHolder) env() *testsuite.TestWorkflowEnvironment {
	return h.NewTestWorkflowEnvironment()
}
