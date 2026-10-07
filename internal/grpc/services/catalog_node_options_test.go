package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// The editor's Tool field on Run Tool (invoke_tool) is a picker, and what it
// offers is exactly the set validation accepts: the tools that opted in to
// node exposure. Naming any other tool is an error, so offering the whole
// registry would offer mostly wrong answers.
func TestListNodes_InvokeToolOffersTheNodeExposedTools(t *testing.T) {
	resp, err := NewCatalogService(nil).ListNodes(context.Background(), connect.NewRequest(&reliantv1.ListNodesRequest{}))
	require.NoError(t, err)

	var tool *reliantv1.NodeInputField
	for _, node := range resp.Msg.GetNodes() {
		if node.GetId() != "invoke_tool" {
			continue
		}
		for _, f := range node.GetInputFields() {
			if f.GetName() == "tool" {
				tool = f
			}
		}
	}
	require.NotNil(t, tool, "invoke_tool.tool is a builder field")
	assert.Equal(t, "node_tool", tool.GetUiHint())

	var want []string
	for _, exposure := range tools.NodeExposedTools() {
		want = append(want, exposure.Tool)
	}
	var got []string
	for _, option := range tool.GetOptions() {
		got = append(got, option.GetValue())
		assert.NotEmpty(t, option.GetDescription(), "%s needs a one-line description for the picker", option.GetValue())
		assert.NotContains(t, option.GetDescription(), "\n")
	}
	assert.Equal(t, want, got)
}

func TestFirstSentence(t *testing.T) {
	assert.Equal(t, "Generate an image.", firstSentence("Generate an image. It costs money."))
	assert.Equal(t, "One line", firstSentence("One line\nSecond line. More."))
	assert.Equal(t, "No end", firstSentence("  No end  "))
}
