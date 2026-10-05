// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func findCallLLM(nodes []*reliantv1.Node) *reliantv1.Node {
	for _, n := range nodes {
		if n.GetCallLlm() != nil {
			return n
		}
		if l := n.GetLoop(); l != nil {
			if found := findCallLLM(l.GetInline().GetNodes()); found != nil {
				return found
			}
		}
	}
	return nil
}

// A model-page thinking level / temperature 0 / compaction threshold must reach
// the call_llm node of a non-agent builtin through the model value alone.
func TestStructuredAgent_ModelPageSettingsReachCallLLM(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("structured-agent.yaml")
	require.NoError(t, err)
	wf, err := runtime.ParseWorkflowProtoBytes(data)
	require.NoError(t, err)
	node := findCallLLM(wf.GetNodes())
	require.NotNil(t, node)

	inputs := map[string]interface{}{
		"model": map[string]interface{}{
			"tags": []interface{}{"flagship"}, "thinking_level": "high",
			"temperature": float64(0), "compaction_threshold": int64(77000),
		},
		"system_prompt": "", "skills": []interface{}{}, "tools": []interface{}{}, "spawn_presets": []interface{}{}, "mode": "auto", "max_turns": int64(0), "ask": false, "response_tool_name": "submit", "response_tool_description": "d", "response_schema": map[string]interface{}{"type": "object"},
	}
	resolved, err := runtime.EvaluateNodeConfig(node, nil, "wf", "structured-agent", inputs, nil, nil, nil)
	require.NoError(t, err)
	args := model.GetCallLLMArgs(resolved)
	ms := model.CelModelSelectorValue(args.GetModel())
	require.NotNil(t, ms)
	require.Equal(t, "high", ms.GetThinkingLevel())
	require.NotNil(t, ms.Temperature)
	require.Equal(t, 0.0, ms.GetTemperature())
	require.EqualValues(t, 77000, ms.GetCompactionThreshold())
}

// No builtin may re-declare a top-level temperature/thinking_level/compaction_threshold
// input that shadows the model value (structured-agent's pin inputs, pitch-deck's
// literal pins and parallel-compete's implementer group are the exceptions).
func TestBuiltins_NoDuplicateModelSettingInputs(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "structured-agent.yaml" {
			continue
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(e.Name())
		require.NoError(t, err)
		wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
		require.NoError(t, err, e.Name())
		for _, dup := range []string{"temperature", "thinking_level", "compaction_threshold"} {
			_, has := wf.GetInputs()[dup]
			require.False(t, has, "%s still declares top-level input %q", e.Name(), dup)
		}
	}
}

func findNodeByID(nodes []*reliantv1.Node, id string) *reliantv1.Node {
	for _, n := range nodes {
		if n.GetId() == id {
			return n
		}
		for _, inner := range [][]*reliantv1.Node{n.GetLoop().GetInline().GetNodes(), n.GetWorkflow().GetInline().GetNodes()} {
			if found := findNodeByID(inner, id); found != nil {
				return found
			}
		}
	}
	return nil
}

// get-it-right's reviewer pins thinking_level high by merging it over the
// user's model value; everything else the user chose must survive.
func TestGetItRight_ReviewerPinsThinkingViaModelValue(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("get-it-right.yaml")
	require.NoError(t, err)
	wf, err := runtime.ParseWorkflowProtoBytes(data)
	require.NoError(t, err)
	review := findNodeByID(wf.GetNodes(), "review")
	require.NotNil(t, review)
	modelArg := review.GetWorkflow().GetArgs()["model"]
	require.NotNil(t, modelArg)

	// Evaluate only the model arg of the real review node.
	probe := &reliantv1.Node{Id: "probe", Type: "workflow", Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
		Ref:  &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://structured-agent"}},
		Args: map[string]*structpb.Value{"model": modelArg},
	}}}
	inputs := map[string]interface{}{"model": map[string]interface{}{
		"tags": []interface{}{"flagship"}, "thinking_level": "low", "temperature": 0.3,
	}}
	resolved, err := runtime.EvaluateNodeConfig(probe, nil, "wf", "get-it-right", inputs, nil, nil, nil)
	require.NoError(t, err)
	got, ok := model.NodeMergedSubWorkflowInputs(resolved)["model"].(map[string]interface{})
	require.True(t, ok, "model arg must resolve to a map, got %#v", model.NodeMergedSubWorkflowInputs(resolved)["model"])
	require.Equal(t, "high", got["thinking_level"])
	require.Equal(t, 0.3, got["temperature"])
	require.Equal(t, []interface{}{"flagship"}, got["tags"])
}
