// Copyright (c) 2025 Reliant Labs
// Tests for tool_choice pinning on the Vertex AI drivers (Gemini and Claude).
package vertexai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"google.golang.org/genai"
)

func TestVertexAIClient_BuildToolConfig(t *testing.T) {
	titleTool := tools.NewSetTitleTool()

	tests := []struct {
		name      string
		force     string
		toolsList []tools.Tool
		wantNil   bool
	}{
		{
			name:      "pins to the named tool",
			force:     tools.SetTitleToolName,
			toolsList: []tools.Tool{titleTool},
			wantNil:   false,
		},
		{
			name:      "unset leaves no tool config",
			force:     "",
			toolsList: []tools.Tool{titleTool},
			wantNil:   true,
		},
		{
			name:      "no tools leaves no tool config",
			force:     tools.SetTitleToolName,
			toolsList: nil,
			wantNil:   true,
		},
		{
			name:      "pin naming an absent tool is dropped",
			force:     "some_other_tool",
			toolsList: []tools.Tool{titleTool},
			wantNil:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &VertexAIClient{options: llm.DriverOptions{ForceToolChoice: tc.force}}
			config := c.buildToolConfig(tc.toolsList)

			if tc.wantNil {
				if config != nil {
					t.Fatalf("expected nil ToolConfig, got %+v", config)
				}
				return
			}

			if config == nil || config.FunctionCallingConfig == nil {
				t.Fatalf("expected a ToolConfig with FunctionCallingConfig, got %+v", config)
			}
			if config.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny {
				t.Fatalf("mode = %v, want ANY", config.FunctionCallingConfig.Mode)
			}
			want := []string{tc.force}
			got := config.FunctionCallingConfig.AllowedFunctionNames
			if len(got) != len(want) || got[0] != want[0] {
				t.Fatalf("AllowedFunctionNames = %v, want %v", got, want)
			}
		})
	}
}

func TestVertexAIClient_BuildGeminiConfig_ToolConfig(t *testing.T) {
	titleTool := tools.NewSetTitleTool()
	c := &VertexAIClient{options: llm.DriverOptions{
		MaxTokens:       256,
		ForceToolChoice: tools.SetTitleToolName,
	}}
	config := c.buildGeminiConfig(nil, []tools.Tool{titleTool})
	if config.ToolConfig == nil || config.ToolConfig.FunctionCallingConfig == nil {
		t.Fatalf("expected ToolConfig to be set on buildGeminiConfig output, got %+v", config.ToolConfig)
	}
	if config.ToolConfig.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny {
		t.Fatalf("mode = %v, want ANY", config.ToolConfig.FunctionCallingConfig.Mode)
	}
}

func TestVertexAIClient_BuildClaudeRequest_ToolChoice(t *testing.T) {
	titleTool := tools.NewSetTitleTool()

	tests := []struct {
		name      string
		force     string
		toolsList []tools.Tool
		wantNil   bool
	}{
		{
			name:      "pins to the named tool",
			force:     tools.SetTitleToolName,
			toolsList: []tools.Tool{titleTool},
			wantNil:   false,
		},
		{
			name:      "unset leaves no tool_choice",
			force:     "",
			toolsList: []tools.Tool{titleTool},
			wantNil:   true,
		},
		{
			name:      "no tools leaves no tool_choice",
			force:     tools.SetTitleToolName,
			toolsList: nil,
			wantNil:   true,
		},
		{
			name:      "pin naming an absent tool is dropped",
			force:     "some_other_tool",
			toolsList: []tools.Tool{titleTool},
			wantNil:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &VertexAIClient{options: llm.DriverOptions{
				MaxTokens:       256,
				ForceToolChoice: tc.force,
			}}
			req := c.buildClaudeRequest(nil, nil, tc.toolsList, false)

			if tc.wantNil {
				if req.ToolChoice != nil {
					t.Fatalf("expected nil ToolChoice, got %+v", req.ToolChoice)
				}
				return
			}

			if req.ToolChoice == nil {
				t.Fatalf("expected ToolChoice to be set")
			}
			if req.ToolChoice.Type != "tool" || req.ToolChoice.Name != tc.force {
				t.Fatalf("ToolChoice = %+v, want type=tool name=%s", req.ToolChoice, tc.force)
			}
		})
	}
}
