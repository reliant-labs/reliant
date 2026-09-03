// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// testPreflightConfig creates a PreflightConfig for testing with known daemon tools.
func testPreflightConfig() *PreflightConfig {
	daemonTools := map[string]bool{
		"bash":        true,
		"bash_list":   true,
		"bash_output": true,
		"bash_kill":   true,
		"fetch":       true,
		"websearch":   true,
	}
	return &PreflightConfig{
		IsDaemonTool: func(name string) bool {
			return daemonTools[name]
		},
		ExpandToolFilter: func(filter []string) []string {
			// Simplified expansion for tests
			var result []string
			for _, spec := range filter {
				switch spec {
				case "tag:default":
					result = append(result, "bash", "view", "edit", "grep", "glob",
						"bash_list", "bash_output", "bash_kill", "fetch", "websearch",
						"create_plan", "update_plan", "get_plan")
				case "tag:planning":
					result = append(result, "create_plan", "update_plan", "get_plan",
						"list_tasks", "add_task", "update_task")
				case "tag:readonly":
					result = append(result, "view", "grep", "glob")
				case "tag:shell":
					result = append(result, "bash")
				default:
					result = append(result, spec) // plain tool name
				}
			}
			return result
		},
	}
}

func TestRequiresDaemon_Nil(t *testing.T) {
	t.Parallel()
	if RequiresDaemon(nil, testPreflightConfig()) {
		t.Error("nil workflow should not require daemon")
	}
}

func TestRequiresDaemon_EmptyWorkflow(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{}
	if RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("empty workflow should not require daemon")
	}
}

func TestRequiresDaemon_WorkflowLevelDaemon(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Daemon: &reliantv1.CelDaemonSelector{
			Value: &reliantv1.CelDaemonSelector_Literal{
				Literal: &reliantv1.DaemonSelectorProto{Type: "local"},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with daemon field should require daemon")
	}
}

func TestRequiresDaemon_RunNode(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "build", Type: "run"},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with run node should require daemon")
	}
}

func TestRequiresDaemon_NodeWithDaemonField(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Daemon: &reliantv1.CelDaemonSelector{
					Value: &reliantv1.CelDaemonSelector_Literal{
						Literal: &reliantv1.DaemonSelectorProto{Type: "cloud"},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with node-level daemon should require daemon")
	}
}

func TestRequiresDaemon_ServerOnlyWorkflow(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"tag:planning"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("server-only workflow (planning tools) should not require daemon")
	}
}

func TestRequiresDaemon_CallLLMWithDefaultTools(t *testing.T) {
	t.Parallel()
	// tag:default includes bash (ToolRunsOnDaemon)
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"tag:default"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with tag:default tools should require daemon (includes bash)")
	}
}

func TestRequiresDaemon_CELToolFilter(t *testing.T) {
	t.Parallel()
	// CEL expressions can't be statically evaluated — conservatively require daemon
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Expr{
									Expr: "inputs.tools",
								},
							},
						},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with CEL tool_filter should conservatively require daemon")
	}
}

func TestRequiresDaemon_InlineSubWorkflow(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "sub",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Inline: &reliantv1.Workflow{
							Nodes: []*reliantv1.Node{
								{Id: "cmd", Type: "run"},
							},
						},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with inline sub-workflow containing run node should require daemon")
	}
}

func TestRequiresDaemon_InlineLoop(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "loop",
				Type: "loop",
				Args: &reliantv1.Node_Loop{
					Loop: &reliantv1.LoopArgs{
						Inline: &reliantv1.Workflow{
							Nodes: []*reliantv1.Node{
								{Id: "cmd", Type: "run"},
							},
						},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with inline loop containing run node should require daemon")
	}
}

func TestRequiresDaemon_CallLLMNoToolFilter(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{},
				},
			},
		},
	}
	// No tool_filter means we can't detect daemon tools statically
	if RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("call_llm with no tool_filter should not be detected as requiring daemon")
	}
}

func TestRequiresDaemon_NilConfig(t *testing.T) {
	t.Parallel()
	// With nil config, tool filter analysis should be conservative
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"bash"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if !RequiresDaemon(wf, nil) {
		t.Error("with nil config, should conservatively assume daemon needed when tool_filter is set")
	}
}

func TestRequiresDaemon_ReadOnlyToolsOnly(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						ToolsConfig: &reliantv1.ToolsConfig{
							Filter: &reliantv1.CelStringList{
								Value: &reliantv1.CelStringList_Literal{
									Literal: &reliantv1.StringList{
										Values: []string{"tag:readonly"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if RequiresDaemon(wf, testPreflightConfig()) {
		t.Error("workflow with only readonly tools should not require daemon")
	}
}
