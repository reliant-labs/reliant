package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func TestParseWorkflowYAML_ParsesV2Workflow(t *testing.T) {
	yamlContent := `
name: test-workflow
description: test description
apiVersion: v2

inputs:
  model:
    type: model
  team:
    type: group
    presets:
      tag: agent
    inputs:
      temperature:
        type: number
        default: 0.7

entry: [llm]
nodes:
  - id: llm
    type: call_llm
    model: "{{inputs.model}}"
    tools: true
`

	workflow, err := parseWorkflowYAML([]byte(yamlContent))
	require.NoError(t, err)
	require.NotNil(t, workflow)

	assert.Equal(t, "test-workflow", workflow.Name)
	assert.Equal(t, "test description", workflow.Description)
	assert.Equal(t, "v2", workflow.ApiVersion)
	require.Contains(t, workflow.Inputs, "team")
	require.Len(t, workflow.Nodes, 1)

	callLLMArgs := model.GetCallLLMArgs(workflow.Nodes[0])
	require.NotNil(t, callLLMArgs)
}

func TestRPCWorkflowToYAML_RoundTripV2(t *testing.T) {
	original := &reliantv1.Workflow{
		Name:        "roundtrip",
		Description: "roundtrip test",
		ApiVersion:  "v2",
		Entry:       []string{"run"},
		Nodes: []*reliantv1.Node{{
			Id:   "run",
			Type: "run",
			Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hello"}}}},
		}},
	}

	yamlBytes, err := rpcWorkflowToYAML(original)
	require.NoError(t, err)

	parsed, err := parseWorkflowYAML(yamlBytes)
	require.NoError(t, err)
	require.NotNil(t, parsed)

	assert.Equal(t, original.Name, parsed.Name)
	assert.Equal(t, original.Description, parsed.Description)
	require.Len(t, parsed.Nodes, 1)
	runArgs := model.GetRunArgs(parsed.Nodes[0])
	require.NotNil(t, runArgs)
	assert.Equal(t, "echo hello", model.CelStringRaw(runArgs.Command))
}

func TestRPCWorkflowHasPresetGroups_V2(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "preset-test",
		Inputs: map[string]*reliantv1.Input{
			"team": {
				Type: "group",
				Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
					Presets: &reliantv1.PresetsConfig{Tag: "agent"},
				}},
			},
		},
	}

	assert.True(t, rpcWorkflowHasPresetGroups(workflow))
	assert.False(t, rpcWorkflowHasPresetGroups(&reliantv1.Workflow{Name: "no-presets"}))
}
