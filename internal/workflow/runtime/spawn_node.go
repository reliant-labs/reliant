// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// This file holds the pure parts of the spawn contract: what a spawn tool
// call's input means and how the synthetic node that runs it is named.
// splitProtoToolCalls, the third piece, lives in workflow.go.

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
	worktree string
	// providers is the spawn call's own provider policy (`providers`), which
	// beats its preset's and what the child would inherit. Nil when absent.
	providers *preset.ProviderRouting
	// providersFromString is set when `providers` arrived as a JSON-encoded
	// string and was decoded to the object it holds (or, blank, read as
	// absent). Builds before spawnStringProvidersVersion refused that shape,
	// so parseSpawnToolCall gates accepting it on a workflow version.
	providersFromString bool
	// strictProvidersErr is what those builds returned for the stringified
	// value, for a replay that must refuse it again.
	strictProvidersErr error
	rawInput           string
}

// parseSpawnToolInput decodes a spawn tool call's input JSON.
//
// Pure, and deliberately free of workflow.Context, so it is unit-testable on
// its own. parseSpawnToolCall wraps this with the logging and workflow-id
// derivation that need the workflow.
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
	worktree, _ := toolInput["worktree"].(string)
	parsed.worktree = strings.TrimSpace(worktree)

	providers := toolInput["providers"]
	if s, ok := providers.(string); ok {
		if obj, ok := decodeStringifiedObject(s); ok {
			_, parsed.strictProvidersErr = preset.ParseProviderRouting(providers)
			parsed.providersFromString = true
			providers = nil
			if obj != nil {
				providers = obj
			}
		}
	}
	routing, err := preset.ParseProviderRouting(providers)
	if err != nil {
		return parsed, err
	}
	parsed.providers = routing

	// Prompt is always required, including for resumptions.
	if parsed.prompt == "" {
		return parsed, fmt.Errorf("spawn tool requires a non-empty 'prompt' parameter")
	}
	return parsed, nil
}

// spawnStringProvidersVersion gates accepting a spawn call's `providers` sent
// as a JSON-encoded string. Before it, the call was refused with a tool error;
// a history recorded then must replay that refusal, so the version is asked
// per tool call, and only for a call whose `providers` was a string.
const spawnStringProvidersVersion workflow.Version = 1

// spawnStringProvidersChangeID prefixes spawnStringProvidersVersionID.
const spawnStringProvidersChangeID = "spawn-string-providers"

// spawnStringProvidersVersionID is the change id gating one spawn call's
// stringified `providers`.
func spawnStringProvidersVersionID(toolCallID string) string {
	return spawnStringProvidersChangeID + "/" + toolCallID
}

// maxStringifiedDepth bounds how many layers of JSON string encoding
// decodeStringifiedObject peels, against pathological inputs.
const maxStringifiedDepth = 5

// decodeStringifiedObject reads an object-typed tool argument a model sent as
// a JSON-encoded string, as models often do with nested arguments:
// "{\"prefer\":[\"codex\"]}" is the object it holds, a string encoded twice is
// peeled twice, and a blank string is the argument left out (nil). It reports
// false for any other string, which stays the caller's to reject.
//
// The registry tools get this from the tools package's schema-guided input
// normalization; spawn is parsed workflow-side, which cannot import that
// package (it imports this one).
func decodeStringifiedObject(s string) (map[string]interface{}, bool) {
	current := s
	for depth := 0; depth < maxStringifiedDepth; depth++ {
		if strings.TrimSpace(current) == "" {
			return nil, true
		}
		var decoded interface{}
		if err := json.Unmarshal([]byte(current), &decoded); err != nil {
			return nil, false
		}
		switch v := decoded.(type) {
		case map[string]interface{}:
			return v, true
		case string:
			current = v
		default:
			return nil, false
		}
	}
	return nil, false
}
