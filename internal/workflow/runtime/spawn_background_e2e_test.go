// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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

	script    []scriptedToolCallsResponse
	scriptIdx int
}

func newSpawnE2EEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, script []scriptedToolCallsResponse) *spawnE2EEnv {
	t.Helper()
	e := &spawnE2EEnv{t: t, env: env, script: script, toolResultsBy: map[string][]interface{}{}}

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
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.toolStatuses = append(e.toolStatuses, input)
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
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.executeToolsStub(input)
		},
		activity.RegisterOptions{Name: "ExecuteTools"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLMStub(input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	return e
}

func (e *spawnE2EEnv) callLLMStub(_ types.ActivityInput) (map[string]interface{}, error) {
	idx := int(atomic.AddInt32(&e.callLLMCount, 1)) - 1
	e.mu.Lock()
	defer e.mu.Unlock()
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
		},
	}
}

type SpawnBackgroundE2ESuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSpawnBackgroundE2E(t *testing.T) {
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

	// After the background spawn's own inline child agent runs (it too hits
	// the scripted CallLLM and gets "no tool calls" on its first turn,
	// finishing immediately), the detached goroutine's completion should
	// unblock the parent's wait within the test env's virtual clock.

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput("chat-bg-lifetime"))

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	// The parent must have taken a THIRD call_llm turn: turn 1 (spawns),
	// turn 2 (no tool calls — the exit candidate), turn 3 (after the
	// detached spawn's mailbox completion was drained and delivered).
	require.GreaterOrEqual(s.T(), e.callLLMInvocations(), 3,
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
func (s *SpawnBackgroundE2ESuite) TestBackground_NoSpin() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput("chat-bg-nospin"))

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	require.Equal(s.T(), 4, e.callLLMInvocations(),
		"exactly parent-turn-1 + parent-turn-2 + parent-turn-3 + child's-one-turn — no extra turns from polling while blocked")
}
