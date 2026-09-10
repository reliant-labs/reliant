// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Spawn modeling for the fast simulator.
//
// The real runtime treats a `spawn` tool call as first class:
// executeToolsWithSpawnSupport splits it out of the ExecuteTools activity
// (splitProtoToolCalls), runs it as a synthetic sub-workflow node named
// "spawn-<toolCallID>" (newSpawnNode), and dispatches it DETACHED — so a loop
// whose `while` has gone false parks in awaitLiveDetachedSpawns and takes
// another turn when a child finishes.
//
// The simulator models all three, reusing the runtime's own split and node
// construction rather than restating them, because the parity lane compares the
// two backends' reached sets as strings and a second copy of the rule would
// drift into exactly the divergence that lane exists to catch.
//
// What is deliberately NOT modeled: concurrency. The simulator has no
// goroutines and no Temporal, so it cannot park. It models the OBSERVABLE
// consequence of parking — the extra turn — which is the only part a scenario's
// reached set or `_iterations` can see.

// spawnToolCallsFromOutput extracts the spawn tool calls an execute_tools node
// was asked to run, from the node's own evaluated args.
//
// The args are read rather than the mock output because that is what the real
// runtime splits: executeToolsWithSpawnSupport reads
// ExecuteToolsArgs.resolved_tool_calls off the CEL-evaluated node, which the
// workflow populated from the preceding call_llm's tool_calls. A scenario that
// mocks a spawn therefore writes it exactly where the LLM would — in call_llm's
// tool_calls — and needs to say nothing about spawn at the execute_tools node.
func spawnToolCallsFromNode(evaluatedNode *reliantv1.Node) []*reliantv1.ToolCallMsg {
	if evaluatedNode.GetType() != model.NodeTypeExecuteTools {
		return nil
	}
	etArgs := evaluatedNode.GetExecuteTools()
	if etArgs == nil {
		return nil
	}
	// The SAME split the real runtime performs, called rather than copied.
	return splitProtoToolCalls(etArgs.GetResolvedToolCalls()).spawnToolCalls
}

// spawnResultToolResult is the tool result a dispatched spawn reports back to
// the LLM. The real runtime returns a handle immediately (the spawn is
// backgrounded) and the child's actual output arrives later via the mailbox, so
// the value here mirrors that handle rather than the child's work.
func spawnResultToolResult(toolCallID, childThread string) map[string]interface{} {
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"content":      fmt.Sprintf("<system>Use agent_id: %s for future resumption</system>\n\nSpawned agent started.", childThread),
		"is_error":     false,
	}
}

// executeSpawnToolCalls runs every spawn tool call an execute_tools node
// produced, as its own synthetic sub-workflow node, and merges the resulting
// tool results into that node's output.
//
// nodePath is the execute_tools node's own qualified path; a spawn node is
// named from the TOOL CALL id alone ("spawn-call_x"), not qualified under its
// dispatching node, because that is what the real runtime does — the spawn is a
// child workflow, not a nested graph node, and its id has to be the same string
// on both lanes for a reached-set comparison to mean anything.
//
// Returns the tool results to merge, so the caller can append them to the
// execute_tools output the way the real buildFinalToolResult does.
func (s *WorkflowSimulator) executeSpawnToolCalls(
	evaluatedNode *reliantv1.Node,
	mocker StepMocker,
) ([]interface{}, error) {
	spawnCalls := spawnToolCallsFromNode(evaluatedNode)
	if len(spawnCalls) == 0 {
		return nil, nil
	}

	var toolResults []interface{}
	for _, spawnCall := range spawnCalls {
		toolCallID := spawnCall.GetId()
		nodeID := spawnNodeID(toolCallID)

		// A malformed spawn is not a run failure: the real runtime returns the
		// parse error to the LLM as a tool result so it can learn and retry,
		// and never dispatches a child. Model the same thing — no node, no
		// detached completion.
		parsed, parseErr := parseSpawnToolInput(spawnCall.GetInput())
		if parseErr != nil {
			toolResults = append(toolResults, map[string]interface{}{
				"tool_call_id": toolCallID,
				"content":      fmt.Sprintf("Spawn failed: %v", parseErr),
				"is_error":     true,
			})
			continue
		}

		// The SAME node the real runtime builds, from the same constructor.
		spawnNode := newSpawnNode(toolCallID, spawnTargetWorkflow, parsed.preset, nil)

		// The transparency gate, unchanged and reused: a spawn targets
		// builtin://agent by REF, so it stays a black box unless the scenario
		// names a node strictly inside it. Executing an unmocked agent ref is
		// what produced the measured 766,072-iteration runaway.
		if s.workflowNodeIsTransparent(nodeID, spawnNode) {
			output, err := s.executeWorkflowNode(nodeID, spawnNode, mocker)
			if err != nil {
				s.markError(nodeID)
				return nil, fmt.Errorf("execute spawn %s: %w", nodeID, err)
			}
			s.nodeOutputs[nodeID] = output
		} else {
			// Black box: the spawn node is mockable as a unit, exactly like any
			// other opaque ref node.
			s.nodeOutputs[nodeID] = mocker(nodeID, map[string]interface{}{
				"preset": parsed.preset,
				"prompt": parsed.prompt,
			})
		}

		s.visitedSteps = append(s.visitedSteps, nodeID)
		s.markCompleted(nodeID)

		// Every spawn dispatches DETACHED (dispatchSpawnBackground), and its
		// completion is what earns the parent loop an extra turn. Recorded on
		// the shared state so a spawn dispatched inside a nested body is
		// visible to the loop that must wait for it.
		s.spawns.pendingCompletions++

		toolResults = append(toolResults, spawnResultToolResult(toolCallID, nodeID))
	}
	return toolResults, nil
}

// mergeSpawnToolResults appends spawn tool results to an execute_tools output,
// mirroring how the real runtime combines regular-tool results with spawn
// results into one activity output.
func mergeSpawnToolResults(output map[string]interface{}, spawnResults []interface{}) {
	if len(spawnResults) == 0 {
		return
	}
	existing, _ := output["tool_results"].([]interface{})
	output["tool_results"] = append(existing, spawnResults...)
}

// awaitDetachedSpawnCompletion is the simulator's model of
// InlineLoopExecutor.awaitLiveDetachedSpawns: it is consulted ONLY when a loop
// is about to exit, and reports whether the loop should take another turn
// because a detached spawn completed.
//
// The real gate blocks until a live child finishes and then re-enters, because
// the child's result may now be in the mailbox and only another turn can
// deliver it. The simulator cannot block — it has no concurrency — so it treats
// a dispatched spawn as having completed by the time the loop tries to exit,
// which is the same observable outcome: one extra turn per completion.
//
// Consuming the count is what makes this terminate. Each spawn is worth exactly
// one re-entry, matching the real gate's snapshot-and-compare on a monotonic
// completion counter, where each completion satisfies the predicate once. A
// re-entered turn that spawns again earns another turn — which is correct, and
// is bounded by the same maxSimulationIterations ceiling every loop already has.
func (s *WorkflowSimulator) awaitDetachedSpawnCompletion() bool {
	if s.spawns == nil || s.spawns.pendingCompletions == 0 {
		return false
	}
	s.spawns.pendingCompletions--
	return true
}

// spawnToolCallsInMockOutput reports whether a call_llm mock emitted any spawn
// tool call. Used by validation-free paths that need the answer without an
// evaluated node.
//
// Kept in terms of splitProtoToolCalls rather than a name comparison so the
// definition of "is a spawn" lives in exactly one place.
func spawnToolCallsInMockOutput(output map[string]interface{}) bool {
	rawToolCalls, ok := output["tool_calls"]
	if !ok {
		return false
	}
	data, err := json.Marshal(rawToolCalls)
	if err != nil {
		return false
	}
	var parsed []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	calls := make([]*reliantv1.ToolCallMsg, 0, len(parsed))
	for _, tc := range parsed {
		calls = append(calls, &reliantv1.ToolCallMsg{Id: tc.ID, Name: tc.Name})
	}
	return len(splitProtoToolCalls(calls).spawnToolCalls) > 0
}
