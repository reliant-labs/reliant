package activities

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

func callLLMWorkflow(preloaded ...string) *reliantv1.Workflow {
	return &reliantv1.Workflow{Nodes: []*reliantv1.Node{{
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			ToolsConfig: &reliantv1.ToolsConfig{
				PreloadedTools: &reliantv1.CelStringList{
					Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: preloaded}},
				},
			},
		}},
	}}}
}

func TestPreflight_MCPToolsRequireDaemon(t *testing.T) {
	cfg := newPreflightConfig()
	for _, tc := range []struct {
		name   string
		filter []string
		want   bool
	}{
		{"named mcp tool", []string{"mcp__chrome-devtools__new_page"}, true},
		{"mcp glob", []string{"mcp__serena__*"}, true},
		{"tag:mcp", []string{"tag:mcp"}, true},
		{"server-only tool", []string{"view"}, false},
		{"mcp exclusion alone", []string{"view", "!mcp__x__y"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := v2.RequiresDaemon(callLLMWorkflow(tc.filter...), cfg); got != tc.want {
				t.Fatalf("RequiresDaemon(%v) = %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

func TestPreflight_IsDaemonToolUsesPlacement(t *testing.T) {
	isDaemon := newPreflightConfig().IsDaemonTool
	for name, want := range map[string]bool{
		"shell":                    true,
		"mcp__serena__find":        true,
		"mcp__*":                   true,
		"fetch":                    false,
		"ask_user":                 false,
		"tool_that_does_not_exist": true, // unresolvable: fail toward needing a daemon
	} {
		if got := isDaemon(name); got != want {
			t.Errorf("IsDaemonTool(%q) = %v, want %v", name, got, want)
		}
	}
}
