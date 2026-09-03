// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Cancelling a background spawn, through the REAL DynamicWorkflow wiring.
//
// spawn_cancel_parked_test.go pins the wait site in isolation. This drives the
// whole path — call_llm dispatches a background spawn, the parent parks on it,
// a cancel_thread signal arrives — and asserts what a user actually observes:
// the run ends, and the cancelled tool call still settles terminally.
//
// The essential property of this harness, and the reason it does not reuse
// spawnE2EEnv, is that the CHILD KEEPS RUNNING. spawnE2EEnv's scripted CallLLM
// returns no tool calls once its script is exhausted, so every spawned child
// finishes on its first turn and the parent never actually parks — a cancel
// test built on it passes whether or not the fix is present, because there was
// no wait to release. Here the child loops forever (it always returns a tool
// call), so the parent is genuinely parked in awaitLiveDetachedSpawns and the
// ONLY thing that can end the run is cancellation being observed at that park.

// cancelE2EEnv wires DynamicWorkflow's activity surface with a CallLLM stub
// that distinguishes the parent thread from spawned children by
// RuntimeContext.Thread.
type cancelE2EEnv struct {
	t   *testing.T
	env *testsuite.TestWorkflowEnvironment

	parentThread string

	mu           sync.Mutex
	toolStatuses []map[string]interface{}
	parentTurns  int
	childTurns   map[string]int
	// threadForToolCall maps a spawn tool call id to the generated thread id
	// of the child it started, so a test can assert on one named spawn.
	threadForToolCall map[string]string
}

// turnsForToolCall returns how many turns the child started by toolCallID has
// taken. Zero if the child has not been created yet.
func (e *cancelE2EEnv) turnsForToolCall(toolCallID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	thread, ok := e.threadForToolCall[toolCallID]
	if !ok {
		return 0
	}
	return e.childTurns[thread]
}

// newCancelE2EEnv builds the harness. parentSpawns is the list of spawn tool
// calls the parent issues on its FIRST turn; afterwards the parent returns no
// tool calls, which is the exit candidate that makes it park on its children.
func newCancelE2EEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, parentThread string, parentSpawns []map[string]interface{}) *cancelE2EEnv {
	t.Helper()
	e := &cancelE2EEnv{
		t:                 t,
		env:               env,
		parentThread:      parentThread,
		childTurns:        map[string]int{},
		threadForToolCall: map[string]string{},
	}

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
	for _, name := range []string{"WorkflowCheckpoint", "WorkflowError", "ThreadStatus"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			// Record which tool call this child thread belongs to. Child
			// thread ids are generated, so this mapping is the only way to
			// assert on a NAMED spawn's progress. The spawn path reports the
			// pairing on its "started" status.
			toolCallID, _ := input["spawned_by_tool_call_id"].(string)
			thread, _ := input["thread"].(string)
			if toolCallID != "" && thread != "" {
				e.mu.Lock()
				e.threadForToolCall[toolCallID] = thread
				e.mu.Unlock()
			}
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.toolStatuses = append(e.toolStatuses, input)
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitToolCallStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "EmitThreadEvent"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (interface{}, error) {
			threadID, _ := input["thread_id"].(string)
			return map[string]interface{}{"message_id": "msg-" + threadID}, nil
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
			return map[string]interface{}{"content": "child result", "is_error": false}, nil
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
			id, _ := input["tool_call_id"].(string)
			return map[string]interface{}{"id": "am-" + id}, nil
		},
		activity.RegisterOptions{Name: "EnqueueAgentMessage"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
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
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			return map[string]interface{}{"tool_results": []interface{}{}}, nil
		},
		activity.RegisterOptions{Name: "ExecuteTools"},
	)

	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			thread := input.Runtime.Thread
			e.mu.Lock()
			defer e.mu.Unlock()

			if thread == e.parentThread {
				e.parentTurns++
				if e.parentTurns == 1 {
					tcJSON, _ := json.Marshal(parentSpawns)
					var tcs []interface{}
					_ = json.Unmarshal(tcJSON, &tcs)
					return map[string]interface{}{"response_text": "spawning", "tool_calls": tcs}, nil
				}
				// Exit candidate: no tool calls. The parent parks on its
				// still-running children here.
				return map[string]interface{}{"response_text": "waiting", "tool_calls": nil}, nil
			}

			// A CHILD turn. Always return a tool call so the child's loop
			// keeps going — it must still be live when the cancel arrives, or
			// the parent would never have parked.
			e.childTurns[thread]++
			busy := []map[string]interface{}{{
				"id":    "child-work",
				"name":  "read_file",
				"input": `{"path":"a.txt"}`,
			}}
			tcJSON, _ := json.Marshal(busy)
			var tcs []interface{}
			_ = json.Unmarshal(tcJSON, &tcs)
			return map[string]interface{}{"response_text": "working", "tool_calls": tcs}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	return e
}

func (e *cancelE2EEnv) statusesFor(toolCallID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, st := range e.toolStatuses {
		if st["tool_call_id"] == toolCallID {
			if s, ok := st["status"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

type SpawnCancelE2ESuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSpawnCancelE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SpawnCancelE2ESuite))
}

// TestCancelBackgroundSpawn_RunEnds is the user-facing regression: a parent
// parked on a background spawn that is still working is cancelled, and the run
// must end.
//
// Without the fix the cancel_thread signal sets the flag, the parked parent
// never looks at it, the child loops forever, and the workflow runs until the
// test environment's deadline — exactly what the user saw as "I clicked cancel
// and it kept running."
func (s *SpawnCancelE2ESuite) TestCancelBackgroundSpawn_RunEnds() {
	env := s.NewTestWorkflowEnvironment()
	// Keep the run bounded so a regression fails fast instead of hanging.
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-cancel-bg"
	parentThread := "thread-" + chatID

	e := newCancelE2EEnv(s.T(), env, parentThread, []map[string]interface{}{
		spawnToolCall("tc1", "long running research"),
	})

	// Cancel the PARENT thread, which is what stops the run that is parked on
	// the spawn.
	//
	// parkedParentTurns records how far the parent had got when the cancel
	// landed. The assertion below uses it to prove this test is not vacuous:
	// the parent must already have taken its exit-candidate turn (turn 2) and
	// be PARKED, because a cancel that arrives while it is still looping would
	// be caught by the ordinary step-boundary check and would pass with or
	// without the fix.
	var parkedParentTurns int
	env.RegisterDelayedCallback(func() {
		e.mu.Lock()
		parkedParentTurns = e.parentTurns
		e.mu.Unlock()
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: parentThread})
	}, 5*time.Second)
	defer func() {
		require.GreaterOrEqual(s.T(), parkedParentTurns, 2,
			"the parent must already be parked on its spawn when the cancel arrives, otherwise "+
				"this test would pass via the step-boundary check and prove nothing")
	}()

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))

	require.True(s.T(), env.IsWorkflowCompleted(),
		"cancelling a run parked on a background spawn must end it; it kept running, which is "+
			"the user-reported bug")
}

// TestCancelOneSpawn_SiblingKeepsRunning is the blast-radius assertion.
//
// Two background spawns are dispatched in the same turn and BOTH are still
// working. Cancelling one child thread must not stop the other: the known
// failure mode here is a cancel aimed at one tool call arriving for every
// sibling (see the comment in execute_tools.go).
//
// The surviving sibling is proven unaffected by the turns it keeps taking
// after the cancel — a broadcast cancel would freeze it at the count it had
// when the signal landed.
func (s *SpawnCancelE2ESuite) TestCancelOneSpawn_SiblingKeepsRunning() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	const chatID = "chat-cancel-sibling"
	parentThread := "thread-" + chatID

	e := newCancelE2EEnv(s.T(), env, parentThread, []map[string]interface{}{
		spawnToolCall("tc1", "cancel this one"),
		spawnToolCall("tc2", "this one must survive"),
	})

	// Cancel ONLY tc1, by tool call id — the id the UI has.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{ToolCallID: "tc1"})
	}, 5*time.Second)

	// Sample each child's progress AFTER the cancel has landed. Both are
	// measured per tool call, not in aggregate: the two children run at
	// different speeds, so a combined total keeps climbing on the survivor's
	// turns alone and would hide the cancelled one failing to stop.
	var tc1TurnsAfterCancel, tc2TurnsAfterCancel int
	// tc2's statuses are sampled HERE, while the run is still going, not after
	// it ends. Ending the run cancels the parent, and a parent terminating
	// with live detached spawns legitimately sweeps them to cancelled
	// (terminalDrainDetachedSpawns) — that is parent teardown, not tc1's
	// cancel leaking, and reading the statuses afterwards would conflate the
	// two.
	var tc2StatusesDuringRun []string
	env.RegisterDelayedCallback(func() {
		tc1TurnsAfterCancel = e.turnsForToolCall("tc1")
		tc2TurnsAfterCancel = e.turnsForToolCall("tc2")
		tc2StatusesDuringRun = e.statusesFor("tc2")
	}, 7*time.Second)

	// End the run so the test terminates.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: parentThread})
	}, 12*time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))

	require.True(s.T(), env.IsWorkflowCompleted())

	// tc1 was the cancel target; tc2 must not have been marked cancelled while
	// the run was still going.
	require.NotContains(s.T(), tc2StatusesDuringRun, "cancelled",
		"cancelling tc1 must not mark its sibling tc2 cancelled — wrong blast radius")

	// The cancelled spawn must have STOPPED: its turn count is frozen from the
	// moment the cancel landed to the end of the run.
	require.Equal(s.T(), tc1TurnsAfterCancel, e.turnsForToolCall("tc1"),
		"the cancelled spawn tc1 must stop taking turns")

	// The sibling must have KEPT GOING over the same window. Together these
	// two assertions are what pin the blast radius: one stopped, one did not.
	require.Greater(s.T(), e.turnsForToolCall("tc2"), tc2TurnsAfterCancel,
		"the uncancelled sibling tc2 must keep taking turns after tc1 was cancelled; work "+
			"stopping everywhere is the wrong blast radius")
}
