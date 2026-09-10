// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// These tests pin the fast simulator's model of the `spawn` tool against what
// the real runtime does (workflow.go's executeToolsWithSpawnSupport,
// splitProtoToolCalls, and InlineLoopExecutor.awaitLiveDetachedSpawns). The
// parity lane compares the two backends' reached sets scenario for scenario,
// so every fact asserted here is a fact that lane would otherwise report as a
// divergence.

// spawnLoopWorkflow is the shape structured-agent's agent_loop has, reduced to
// the parts a spawn exercises: an LLM turn that may emit tool calls, and an
// execute_tools node that receives them.
//
// The `while` exits as soon as execute_tools reports done, so a loop that takes
// an extra turn can only have taken it for the reason under test.
const spawnLoopWorkflow = `
name: spawn-loop
apiVersion: "1.0"
entry: [agent_loop]
outputs:
  done: "{{nodes.agent_loop.done}}"
nodes:
  - id: agent_loop
    type: loop
    while: outputs.done != true
    inline:
      entry: [call_llm]
      outputs:
        done: "{{has(nodes.execute_tools) && has(nodes.execute_tools.response_data) && nodes.execute_tools.response_data != null && 'finish' in nodes.execute_tools.response_data}}"
      nodes:
        - id: call_llm
          type: call_llm
          model:
            tags: [fast]
        - id: execute_tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0
`

// spawnTargetInnerWorkflow stands in for builtin://agent — the workflow every
// spawn tool call targets. Two nodes, so a scenario that reaches them proves
// the body really ran instead of being mocked as a unit.
const spawnTargetInnerWorkflow = `
name: agent
apiVersion: "1.0"
entry: [call_llm]
outputs:
  response_text: "{{nodes.call_llm.response_text}}"
nodes:
  - id: call_llm
    type: call_llm
    model:
      tags: [fast]
  - id: save
    type: save_message
    args:
      role: "assistant"
      content: "inner"
edges:
  - from: call_llm
    default: save
`

func spawnEngine(t *testing.T) *Engine {
	t.Helper()
	wf, err := ParseWorkflowYAML([]byte(spawnLoopWorkflow))
	require.NoError(t, err)
	inner, err := ParseWorkflowYAML([]byte(spawnTargetInnerWorkflow))
	require.NoError(t, err)
	return NewEngineWithLoader(wf, func(string) (*reliantv1.Workflow, error) { return inner, nil })
}

// spawnCall is a call_llm mock output carrying one spawn tool call.
func spawnCall(id, preset string) map[string]interface{} {
	return map[string]interface{}{
		"response_text": "spawning",
		"tool_calls": []interface{}{
			map[string]interface{}{
				"id":    id,
				"name":  "spawn",
				"input": `{"preset":"` + preset + `","prompt":"investigate"}`,
			},
		},
	}
}

// doneOutput is an execute_tools result that satisfies the loop's exit
// condition, in the shape the real ExecuteTools activity produces: the tool's
// result keyed by tool name under response_data.
func doneOutput() map[string]interface{} {
	return map[string]interface{}{
		"response_data": map[string]interface{}{"finish": map[string]interface{}{"ok": true}},
	}
}

func reachedSet(result *ScenarioResult) map[string]bool {
	set := map[string]bool{}
	for _, node := range result.Execution.NodesReached {
		set[node] = true
	}
	return set
}

// TestSpawnProducesSyntheticNode is the reached-set half of parity. The real
// runtime splits a `spawn` call out of execute_tools and runs it as a synthetic
// sub-workflow node named "spawn-" + the tool call id (workflow.go's
// newSpawnNode). A simulator that never produces that id cannot agree with the
// Temporal lane no matter what a scenario asserts, because the id is not one
// the workflow author wrote and so cannot be supplied by hand.
func TestSpawnProducesSyntheticNode(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "spawn_produces_node",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: spawnCall("call_spawn", "researcher")},
			{Node: "agent_loop.execute_tools", Output: map[string]interface{}{}},
			// The turn the completed detached spawn earns.
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "done",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_finish", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
			// The detached spawn completes AFTER the exit condition is first
			// satisfied, so the loop takes one more turn to react to it.
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "reacting to the spawn result",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_after_spawn", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
	})

	require.Equal(t, "completed", result.Execution.Outcome, "mismatches: %v", result.Mismatches)
	require.True(t, reachedSet(result)["spawn-call_spawn"],
		"expected the synthetic spawn node in reached (got %v)", result.Execution.NodesReached)
}

// TestUnmockedSpawnTargetStaysBlackBoxed pins the transparency gate on the
// spawn path. A spawn targets builtin://agent by REF, and an unmocked ref must
// stay opaque: executing one is what previously produced a measured
// 766,072-iteration runaway, because the agent loop waits on a completion
// signal no mock supplies and max_turns 0 means unlimited.
//
// The scenario names no node inside the spawn, so the spawn's body must not
// run and its internal ids must not appear in reached.
func TestUnmockedSpawnTargetStaysBlackBoxed(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "spawn_target_black_boxed",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: spawnCall("call_spawn", "researcher")},
			{Node: "agent_loop.execute_tools", Output: map[string]interface{}{}},
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "done",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_finish", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
			// The detached spawn completes AFTER the exit condition is first
			// satisfied, so the loop takes one more turn to react to it.
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "reacting to the spawn result",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_after_spawn", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
	})

	require.Equal(t, "completed", result.Execution.Outcome, "mismatches: %v", result.Mismatches)
	reached := reachedSet(result)
	require.True(t, reached["spawn-call_spawn"], "the spawn node itself must be reached")
	require.False(t, reached["spawn-call_spawn.call_llm"],
		"an unmocked spawn target must stay a black box (reached: %v)", result.Execution.NodesReached)
	require.False(t, reached["spawn-call_spawn.save"],
		"an unmocked spawn target must stay a black box (reached: %v)", result.Execution.NodesReached)
}

// TestMockedSpawnTargetIsTransparent is the other side of the same gate: a
// scenario that names nodes strictly inside the spawn is asking for the body,
// and gets it. Without this the gate would be a wall rather than a default.
func TestMockedSpawnTargetIsTransparent(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "spawn_target_transparent",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: spawnCall("call_spawn", "researcher")},
			{Node: "spawn-call_spawn.call_llm", Output: map[string]interface{}{"response_text": "inner ran"}},
			{Node: "spawn-call_spawn.save", Output: map[string]interface{}{}},
			{Node: "agent_loop.execute_tools", Output: map[string]interface{}{}},
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "done",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_finish", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
			// The detached spawn completes AFTER the exit condition is first
			// satisfied, so the loop takes one more turn to react to it.
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "reacting to the spawn result",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_after_spawn", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
	})

	require.Equal(t, "completed", result.Execution.Outcome, "mismatches: %v", result.Mismatches)
	reached := reachedSet(result)
	require.True(t, reached["spawn-call_spawn.call_llm"],
		"a spawn target the scenario mocked internally must execute (reached: %v)", result.Execution.NodesReached)
	require.True(t, reached["spawn-call_spawn.save"],
		"a spawn target the scenario mocked internally must execute (reached: %v)", result.Execution.NodesReached)
}

// TestDetachedSpawnReEntersLoop pins the loop-lifetime rule. Every spawn
// dispatches DETACHED, and a loop whose `while` has already gone false parks in
// awaitLiveDetachedSpawns rather than exiting; a completed detached spawn wakes
// it and the loop takes ANOTHER turn ("Detached spawn(s) completed, re-entering
// loop", loop_executor.go).
//
// So the honest iteration count for a turn that spawned is one higher than the
// turn on which the exit condition was first satisfied. Here `done: true`
// lands on turn 1, so a loop that ignored detached spawns would report
// _iterations 1; the runtime reports 2.
func TestDetachedSpawnReEntersLoop(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "detached_spawn_re_enters",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: spawnCall("call_spawn", "researcher")},
			// The exit condition is satisfied on the FIRST turn.
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
			// The extra turn the completed detached spawn earns.
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "reacting to the spawn result",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_finish", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
		Expect: &Expectation{
			Outcome: "completed",
			NodeOutputs: map[string]map[string]interface{}{
				"agent_loop": {"_iterations": 2},
			},
		},
	})

	require.Equal(t, StatusPassed, result.Status,
		"a completed detached spawn must earn one extra loop turn; mismatches: %v", result.Mismatches)
}

// TestSpawnNodeIDPrefixMatchesRuntime pins validation's copy of the spawn node
// id prefix against the ids the runtime actually produces. The constant is
// unexported in the runtime package, so the pairing is asserted through a real
// run rather than by comparing the two literals: a run that spawns must yield a
// node id validation would accept.
func TestSpawnNodeIDPrefixMatchesRuntime(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "spawn_prefix_pin",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: spawnCall("call_spawn", "researcher")},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "after",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_after_spawn", "name": "finish", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
	})

	var spawnNodes []string
	for _, node := range result.Execution.NodesReached {
		if isSpawnNodeRef(node) {
			spawnNodes = append(spawnNodes, node)
		}
	}
	require.Equal(t, []string{"spawn-call_spawn"}, spawnNodes,
		"validation's spawn prefix must match the ids the runtime produces (reached: %v)",
		result.Execution.NodesReached)
}

// TestNoSpawnNoExtraTurn is the control. The re-entry must be caused by the
// spawn, not by the exit check being loosened for everyone: an identical loop
// with an ordinary tool call ends on the turn its `while` goes false.
func TestNoSpawnNoExtraTurn(t *testing.T) {
	result := spawnEngine(t).RunScenario(&Scenario{
		Name: "no_spawn_no_extra_turn",
		Events: []SimulatedEvent{
			{Node: "agent_loop.call_llm", Output: map[string]interface{}{
				"response_text": "working",
				"tool_calls": []interface{}{
					map[string]interface{}{"id": "call_grep", "name": "grep", "input": "{}"},
				},
			}},
			{Node: "agent_loop.execute_tools", Output: doneOutput()},
		},
		Expect: &Expectation{
			Outcome: "completed",
			NodeOutputs: map[string]map[string]interface{}{
				"agent_loop": {"_iterations": 1},
			},
		},
	})

	require.Equal(t, StatusPassed, result.Status,
		"a loop with no spawn must exit on the turn its while goes false; mismatches: %v", result.Mismatches)
}
