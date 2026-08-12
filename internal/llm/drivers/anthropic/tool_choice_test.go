// Copyright (c) 2025 Reliant Labs
// Tests for tool_choice pinning on the Anthropic drivers.
//
// Pinning is what makes chat title generation reliable: it forces the model's
// first emission to be a tool_use block, so it cannot narrate ("I'll
// investigate...") in response to the Claude Code system prompt's instruction
// to say what it is about to do before acting.
package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// toolChoiceFromParams reads back the serialized tool_choice object.
func toolChoiceFromParams(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return body.ToolChoice
}

func TestAnthropicClient_ToolChoicePin(t *testing.T) {
	tests := []struct {
		name     string
		force    string
		wantType string
		wantName string
	}{
		{name: "unset leaves provider default", force: ""},
		{
			name:     "pins to the named tool",
			force:    tools.SetTitleToolName,
			wantType: "tool",
			wantName: tools.SetTitleToolName,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewAnthropicClientWithOptions(llm.DriverOptions{
				Model:           models.Model{APIModel: "claude-haiku-4-5"},
				MaxTokens:       256,
				ForceToolChoice: tc.force,
			})
			params := client.preparedMessages(nil, nil, nil)
			raw, err := params.MarshalJSON()
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			choice := toolChoiceFromParams(t, raw)
			if tc.wantType == "" {
				if len(choice) != 0 {
					t.Fatalf("expected no tool_choice, got %v", choice)
				}
				return
			}
			if choice["type"] != tc.wantType || choice["name"] != tc.wantName {
				t.Fatalf("tool_choice = %v, want type=%s name=%s", choice, tc.wantType, tc.wantName)
			}
			// Exactly one tool_use block, so the title can be read from a
			// single unambiguous call.
			if choice["disable_parallel_tool_use"] != true {
				t.Fatalf("expected disable_parallel_tool_use=true, got %v", choice)
			}
		})
	}
}

// The Claude Code driver renames tools with an mcp__ prefix. tool_choice must
// use the SAME rewritten name, or the pin names a tool absent from the request
// and the API rejects it.
func TestClaudeCodeClient_ToolChoiceUsesMCPPrefixedName(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-haiku-4-5"},
		MaxTokens:       256,
		ForceToolChoice: tools.SetTitleToolName,
	})

	titleTool := tools.NewSetTitleTool()
	converted := client.convertTools([]tools.Tool{titleTool})
	if len(converted) != 1 || converted[0].OfTool == nil {
		t.Fatalf("expected one converted tool, got %v", converted)
	}
	toolNameInRequest := converted[0].OfTool.Name

	params := client.preparedMessages(nil, nil, converted)
	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	choice := toolChoiceFromParams(t, raw)
	if choice["name"] != toolNameInRequest {
		t.Fatalf("tool_choice name = %v, want %q (the name used in the tools array)",
			choice["name"], toolNameInRequest)
	}
	if toolNameInRequest != mcpToolPrefix+tools.SetTitleToolName {
		t.Fatalf("expected mcp-prefixed tool name, got %q", toolNameInRequest)
	}
}

func TestClaudeCodeClient_ToolChoiceOmittedWhenUnset(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-haiku-4-5"},
		MaxTokens: 4096,
	})
	params := client.preparedMessages(nil, nil, nil)
	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if choice := toolChoiceFromParams(t, raw); len(choice) != 0 {
		t.Fatalf("expected no tool_choice for normal agent turns, got %v", choice)
	}
}
