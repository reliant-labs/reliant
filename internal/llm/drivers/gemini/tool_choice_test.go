// Copyright (c) 2025 Reliant Labs
// Tests for tool_choice pinning on the Gemini driver.
package gemini

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"google.golang.org/genai"
)

func TestGeminiClient_BuildToolConfig(t *testing.T) {
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
			g := &GeminiClient{options: llm.DriverOptions{ForceToolChoice: tc.force}}
			config := g.buildToolConfig(tc.toolsList)

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
