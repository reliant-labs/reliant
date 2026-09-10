// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

// This file holds the parts of the spawn contract that BOTH execution lanes
// need: the real Temporal runtime (workflow.go) and the fast simulator
// (simulator.go). They are here rather than duplicated because the two lanes
// are compared scenario-for-scenario by the parity test, and a second copy of
// "what a spawn is" would drift into exactly the divergence that test exists
// to catch. splitProtoToolCalls is the third shared piece and already lives in
// workflow.go, in this same package.

// spawnNodeIDPrefix is prepended to a spawn tool call's id to name the
// synthetic sub-workflow node the spawn executes as. A spawn is not a node the
// workflow author wrote, so it needs an id no graph can collide with, and both
// lanes must derive the SAME one — a reached-set comparison is string equality.
const spawnNodeIDPrefix = "spawn-"

// spawnNodeID names the synthetic sub-workflow node for one spawn tool call.
func spawnNodeID(toolCallID string) string {
	return spawnNodeIDPrefix + toolCallID
}

// spawnTargetWorkflow is the workflow every spawn tool call runs. The spawned
// agent's behaviour is selected by its PRESET, not by a different graph, so
// this is a constant rather than something read off the tool call.
const spawnTargetWorkflow = "builtin://agent"

// newSpawnNode builds the synthetic `workflow` node a spawn tool call executes
// as. Spawn nodes carry no CEL — every value here is already resolved — so the
// node can be handed to an executor as-is.
func newSpawnNode(toolCallID, targetWorkflow, presetName string, childInputs map[string]interface{}) *reliantv1.Node {
	node := &reliantv1.Node{
		Id:   spawnNodeID(toolCallID),
		Type: model.NodeTypeWorkflow,
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: targetWorkflow}},
		}},
	}
	if childInputs != nil {
		protoArgs := make(map[string]*structpb.Value)
		for key, value := range childInputs {
			if val, err := structpb.NewValue(value); err == nil {
				protoArgs[key] = val
			}
		}
		node.GetWorkflow().Args = protoArgs
	}
	if presetName != "" {
		node.GetWorkflow().Presets = map[string]string{DefaultPresetGroup: presetName}
	}
	return node
}

// spawnToolInput is one spawn tool call's parsed arguments.
type spawnToolInput struct {
	prompt   string
	preset   string
	agentID  string
	title    string
	rawInput string
}

// parseSpawnToolInput decodes a spawn tool call's input JSON.
//
// Pure, and deliberately free of workflow.Context: the simulator has no
// Temporal context and must reach the same decision about what a spawn tool
// call means. The real runtime's parseSpawnToolCall wraps this with the
// logging and workflow-id derivation only it can do.
//
// The input may arrive wrapped in a metadata envelope
// ({"input": "<raw>", "__reliant_tool_meta__": {...}}); the envelope is
// unwrapped so callers see the arguments the LLM actually wrote.
func parseSpawnToolInput(input string) (spawnToolInput, error) {
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(input), &envelope); err != nil {
		return spawnToolInput{}, fmt.Errorf("parse spawn tool input: %w", err)
	}
	if _, hasMeta := envelope["__reliant_tool_meta__"]; hasMeta {
		if rawInput, ok := envelope["input"].(string); ok {
			input = rawInput
		}
	}

	var toolInput map[string]interface{}
	if err := json.Unmarshal([]byte(input), &toolInput); err != nil {
		return spawnToolInput{}, fmt.Errorf("parse unwrapped spawn tool input: %w", err)
	}

	parsed := spawnToolInput{rawInput: input}
	parsed.prompt, _ = toolInput["prompt"].(string)
	parsed.preset, _ = toolInput["preset"].(string)
	parsed.agentID, _ = toolInput["agent_id"].(string)
	parsed.title, _ = toolInput["title"].(string)

	// Prompt is always required, including for resumptions.
	if parsed.prompt == "" {
		return parsed, fmt.Errorf("spawn tool requires a non-empty 'prompt' parameter")
	}
	return parsed, nil
}
