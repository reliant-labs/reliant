// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// networkOnlyPreflightConfig mirrors the real registry after fetch and websearch
// stopped being daemon-located. Unlike testPreflightConfig, which hardcodes the
// old marking, this one encodes the corrected fact so the assertion below is
// about RequiresDaemon's behaviour rather than about the fixture.
func networkOnlyPreflightConfig() *PreflightConfig {
	daemonTools := map[string]bool{
		"shell":        true,
		"shell_list":   true,
		"shell_output": true,
		"shell_kill":   true,
		"code_context": true,
	}
	return &PreflightConfig{
		IsDaemonTool: func(name string) bool { return daemonTools[name] },
		ExpandToolFilter: func(filter []string) []string {
			var result []string
			for _, spec := range filter {
				switch spec {
				case "tag:web":
					result = append(result, "fetch", "websearch")
				case "tag:readonly":
					result = append(result, "view", "fetch", "websearch")
				default:
					result = append(result, spec)
				}
			}
			return result
		},
	}
}

// TestRequiresDaemon_NetworkOnlyAgentNeedsNoDaemon is the workflow-level point of
// the location change: an agent that can read the web and nothing else must be
// able to start with no daemon connected.
//
// Before fetch/websearch moved off the daemon this returned true, so the
// preflight gate refused the run — the failure the daemon-less work exists to
// remove.
func TestRequiresDaemon_NetworkOnlyAgentNeedsNoDaemon(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "research",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"tag:web"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if RequiresDaemon(wf, networkOnlyPreflightConfig()) {
		t.Error("a workflow whose only tools are fetch/websearch must not require a daemon")
	}
}

// TestRequiresDaemon_ShellStillRequiresDaemon guards the other half: relaxing the
// network tools must not relax the gate for tools that genuinely need a machine.
func TestRequiresDaemon_ShellStillRequiresDaemon(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "build",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"shell"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if !RequiresDaemon(wf, networkOnlyPreflightConfig()) {
		t.Error("a workflow using the shell must still require a daemon")
	}
}
