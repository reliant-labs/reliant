// Copyright (c) 2025 Reliant Labs
package local

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
)

func schemaOnlyTool(name string) llmtools.Tool {
	return llmtools.NewSchemaOnlyTool(name, "stub", map[string]interface{}{"type": "object"})
}

func TestPreparedParams_PinsToolChoiceWhenForcedAndPresent(t *testing.T) {
	client := &LocalClient{Options: llm.DriverOptions{ForceToolChoice: "set_title"}}

	params := client.preparedParams(nil, client.ConvertTools([]llmtools.Tool{schemaOnlyTool("set_title")}))

	if params.ToolChoice.OfFunctionToolChoice == nil {
		t.Fatalf("expected tool_choice to be pinned to set_title")
	}
	if got := params.ToolChoice.OfFunctionToolChoice.Function.Name; got != "set_title" {
		t.Fatalf("tool_choice function name = %q, want set_title", got)
	}
}

func TestPreparedParams_OmitsToolChoiceWhenUnset(t *testing.T) {
	client := &LocalClient{Options: llm.DriverOptions{}}

	params := client.preparedParams(nil, client.ConvertTools([]llmtools.Tool{schemaOnlyTool("set_title")}))

	if params.ToolChoice.OfFunctionToolChoice != nil {
		t.Fatalf("tool_choice should be unset when ForceToolChoice is empty")
	}
}

func TestPreparedParams_OmitsToolChoiceWhenNamedToolAbsent(t *testing.T) {
	client := &LocalClient{Options: llm.DriverOptions{ForceToolChoice: "set_title"}}

	params := client.preparedParams(nil, client.ConvertTools([]llmtools.Tool{schemaOnlyTool("other_tool")}))

	if params.ToolChoice.OfFunctionToolChoice != nil {
		t.Fatalf("tool_choice should not pin to a tool absent from the request's tool list")
	}
}

func TestPreparedParams_OmitsToolChoiceWhenToolsEmpty(t *testing.T) {
	client := &LocalClient{Options: llm.DriverOptions{ForceToolChoice: "set_title"}}

	params := client.preparedParams(nil, nil)

	if params.Tools != nil {
		t.Fatalf("expected no tools to be set")
	}
	if params.ToolChoice.OfFunctionToolChoice != nil {
		t.Fatalf("tool_choice should not pin when the tool list is empty")
	}
}
