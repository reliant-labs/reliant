// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
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
	// llmGrants[thread] records the load_tool grants each call was handed.
	llmGrants map[string][][]string
	// llmPins[thread] records the catalog pin each call was handed.
	llmPins map[string][]string
	// loadPin is the catalog pin ActivityLoadWorkflow reports pinning.
	loadPin string
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
		llmGrants:     map[string][][]string{},
		llmPins:       map[string][]string{},
	}
	wf, err := wfyaml.ParseWorkflow([]byte(spawnBackgroundE2EYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	ok := func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return LoadedWorkflow{WorkflowJSON: wfJSON, CatalogPin: e.loadPin}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadPresetParamsInput) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "LoadPresetParams"})
	for _, name := range []string{"WorkflowStatus", "Cleanup", "EmitThreadEvent", "WorkflowError", "ValidateThreadOwnership", "SaveMessage"} {
		name := name
		switch name {
		case "ValidateThreadOwnership":
			env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"valid": true}, nil
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
	e.llmGrants[thread] = append(e.llmGrants[thread], input.Runtime.ToolGrants)
	e.llmPins[thread] = append(e.llmPins[thread], input.Runtime.CatalogPin)
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

// pressureDial is a history pressure a test turns while the workflow reads it.
// A test turns it from inside CallLLM, which runs on an activity goroutine,
// while the workflow reads it from a coroutine on another — so it is atomic.
type pressureDial struct{ level atomic.Int64 }

func newPressureDial(p historyPressure) *pressureDial {
	d := &pressureDial{}
	d.set(p)
	return d
}

func (d *pressureDial) set(p historyPressure) { d.level.Store(int64(p)) }
func (d *pressureDial) get() historyPressure  { return historyPressure(d.level.Load()) }

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

	var suite1 temporaltest.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	parent := "thread-" + chatID
	e1.scripts[parent] = append([]scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-spawn", "long research")}},
	}, repeatTurns("p", 10)...)
	e1.scripts[child] = repeatTurns("c", 10)

	pressure := newPressureDial(historyPressureNone)
	forcePressure(t, chatID, pressure.get)
	e1.onLLM = func(thread string, n int) {
		// The spawn is two iterations in: history crosses the threshold.
		if thread == child && n == 2 {
			pressure.set(historyPressureSoft)
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
	pressure.set(historyPressureNone)
	var suite2 temporaltest.WorkflowTestSuite
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

	var suite1 temporaltest.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	e1.scripts[parent] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-spawn", "long research")}},
		{toolCalls: nil}, // parent parks in awaitLiveDetachedSpawns
	}
	e1.scripts[child] = repeatTurns("c", 10)

	pressure := newPressureDial(historyPressureNone)
	forcePressure(t, chatID, pressure.get)
	e1.onLLM = func(thread string, n int) {
		if thread == child && n == 3 {
			pressure.set(historyPressureSoft)
		}
	}

	env1.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	carried := carriedInput(t, env1)
	require.Equal(t, 2, e1.llmCalls[parent], "the parent was parked, not taking turns")
	require.True(t, carried.Resume.AwaitSpawnsFirst, "the successor must re-park on the spawn")
	require.Len(t, carried.Resume.Spawns, 1)

	pressure.set(historyPressureNone)
	var suite2 temporaltest.WorkflowTestSuite
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
	pressure := newPressureDial(historyPressureSoft)
	forcePressure(t, chatID, pressure.get)

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
		pressure.set(historyPressureHard)
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
	var suite temporaltest.WorkflowTestSuite
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

	var suite temporaltest.WorkflowTestSuite
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
	var suite temporaltest.WorkflowTestSuite
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

type testsuiteHolder struct{ temporaltest.WorkflowTestSuite }

func (h *testsuiteHolder) env() *testsuite.TestWorkflowEnvironment {
	return h.NewTestWorkflowEnvironment()
}

// ── Deploy of the 10k threshold onto a long multi-spawn chat ──────────────────

// The shape of adcee226 when the lowered threshold deploys: a chat already at
// 15k events (past the new 10k soft threshold, short of the legacy 40k) whose
// root has two background spawns mid-loop. Driven through the REAL gate
// (lowerContinueAsNewChangeID, no pressure override): the handoff is
// requested, both spawns park at their own boundaries, the root hands off
// carrying both, and the successor relaunches each on its own thread and tool
// call, finishes them, and delivers exactly one completion apiece.
func TestContinueAsNew_MultiSpawnChatPastLoweredThresholdHandsOff(t *testing.T) {
	t.Parallel()
	const chatID = "chat-can-10k-multi"
	childA, childB := canChildThread("tc-a"), canChildThread("tc-b")
	parent := "thread-" + chatID

	// History is short until both spawns are two iterations in, then jumps to
	// 15k: the deploy lands on a run that is already mid-spawn.
	var length atomic.Int64
	length.Store(500)
	historySizeOverrides.Store(chatID, func() (int, int) { return int(length.Load()), oneMB })
	t.Cleanup(func() { historySizeOverrides.Delete(chatID) })

	var suite1 temporaltest.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	e1.onLLM = func(thread string, n int) {
		if (thread == childA || thread == childB) && n == 2 {
			length.Store(15000)
		}
	}
	e1.scripts[parent] = append([]scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-a", "research A"), spawnToolCall("tc-b", "research B")}},
	}, repeatTurns("p", 10)...)
	e1.scripts[childA] = repeatTurns("a", 10)
	e1.scripts[childB] = repeatTurns("b", 10)

	env1.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	carried := carriedInput(t, env1)

	require.NotNil(t, carried.Resume)
	require.Len(t, carried.Resume.Spawns, 2, "both live spawns must cross the boundary")
	byThread := map[string]SpawnHandoff{}
	for _, h := range carried.Resume.Spawns {
		byThread[h.ChildThread] = h
		require.Equal(t, parent, h.ParentThread)
	}
	require.Equal(t, "tc-a", byThread[childA].ToolCallID)
	require.Equal(t, "tc-b", byThread[childB].ToolCallID)
	require.Less(t, e1.llmCalls[childA], 10, "spawn A parked mid-script, not run to completion")
	require.Less(t, e1.llmCalls[childB], 10, "spawn B parked mid-script, not run to completion")
	for _, tc := range []string{"tc-a", "tc-b"} {
		require.Zero(t, e1.enqueuedFor(tc), "a parked spawn reports nothing in the predecessor")
		require.NotContains(t, e1.toolStatusesFor(tc), "completed")
		require.NotContains(t, e1.toolStatusesFor(tc), "failed")
	}

	// Successor: fresh history; each relaunched spawn runs out a short script.
	length.Store(50)
	var suite2 temporaltest.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	e2 := newCANSpawnEnv(t, env2)
	e2.scripts[childA] = repeatTurns("ra", 1)
	e2.scripts[childB] = repeatTurns("rb", 2)

	env2.ExecuteWorkflow(DynamicWorkflow, carried)
	require.True(t, env2.IsWorkflowCompleted())
	require.NoError(t, env2.GetWorkflowError())
	for thread, tc := range map[string]string{childA: "tc-a", childB: "tc-b"} {
		require.NotContains(t, e2.createdRows, thread, "the relaunch reuses the existing thread")
		require.Equal(t, byThread[thread].LoopIteration, e2.llmIterations[thread][0],
			"%s resumes at the iteration it parked at", tc)
		require.Equal(t, 1, e2.enqueuedFor(tc), "the parent receives exactly one completion for %s", tc)
		require.Contains(t, e2.toolStatusesFor(tc), "completed", "%s ends completed, not stranded at backgrounded", tc)
	}
}

// Every call_llm turn of a pinned run, the root's and a spawned sub-agent's,
// carries the run's catalog pin: the spawn tool describes the presets from
// it, the same catalog the spawn then loads its preset from.
func TestCallLLMTurnsCarryTheRunsCatalogPin(t *testing.T) {
	t.Parallel()
	const chatID = "chat-llm-catalog-pin"
	child := canChildThread("tc-spawn")
	parent := "thread-" + chatID

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	e := newCANSpawnEnv(t, env)
	e.loadPin = "pin-v1"
	e.scripts[parent] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-spawn", "research")}},
	}
	e.scripts[child] = repeatTurns("c", 1)

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	e.mu.Lock()
	defer e.mu.Unlock()
	require.NotEmpty(t, e.llmPins[parent])
	require.NotEmpty(t, e.llmPins[child], "the spawned sub-agent takes a turn")
	for thread, pins := range map[string][]string{"root": e.llmPins[parent], "sub-agent": e.llmPins[child]} {
		for _, pin := range pins {
			assert.Equal(t, "pin-v1", pin, "%s turn carries the run's pin", thread)
		}
	}
}
