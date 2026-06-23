// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Helper to build a simple call_llm node
func callLLMNode(id string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:   id,
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
	}
}

// Helper to build a simple edge with default targets
func defaultEdge(from string, targets ...string) *reliantv1.Edge {
	return &reliantv1.Edge{From: from, Default: targets}
}

// Helper to build an edge with cases
func casesEdge(from string, cases []*reliantv1.EdgeCase, defaults ...string) *reliantv1.Edge {
	return &reliantv1.Edge{From: from, Cases: cases, Default: defaults}
}

// =============================================================================
// UNREACHABLE NODE DETECTION TESTS
// =============================================================================

func TestUnreachableNodeDetection(t *testing.T) {
	tests := []struct {
		name        string
		workflow    *reliantv1.Workflow
		wantErr     bool
		errContains string
	}{
		{
			name: "all nodes reachable - linear",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("node2"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("node1", "node2"),
				},
			},
			wantErr: false,
		},
		{
			name: "all nodes reachable - branching",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("node2"),
					callLLMNode("node3"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("node1", "node2", "node3"),
				},
			},
			wantErr: false,
		},
		{
			name: "all nodes reachable - multiple entry points",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1", "node2"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("node2"),
				},
				Edges: []*reliantv1.Edge{},
			},
			wantErr: false,
		},
		{
			name: "unreachable node - no edge to it",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("node2"),
					callLLMNode("orphan"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("node1", "node2"),
				},
			},
			wantErr:     true,
			errContains: "node 'orphan' is unreachable",
		},
		{
			name: "multiple unreachable nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("orphan1"),
					callLLMNode("orphan2"),
				},
				Edges: []*reliantv1.Edge{},
			},
			wantErr:     true,
			errContains: "is unreachable",
		},
		{
			name: "unreachable island - connected to each other but not entry",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("island1"),
					callLLMNode("island2"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("island1", "island2"),
				},
			},
			wantErr:     true,
			errContains: "is unreachable",
		},
		{
			name: "reachable via conditional edge",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"node1"},
				Nodes: []*reliantv1.Node{
					callLLMNode("node1"),
					callLLMNode("node2"),
					callLLMNode("node3"),
				},
				Edges: []*reliantv1.Edge{
					casesEdge("node1",
						[]*reliantv1.EdgeCase{
							{Condition: "true", To: []string{"node2"}},
						},
						"node3",
					),
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StaticAnalysis(tt.workflow, nil)

			// Filter to only reachability errors (ignore model/field validation)
			var reachabilityErrors []string
			for _, err := range result.Errors() {
				if strings.Contains(err.Message, "unreachable") {
					reachabilityErrors = append(reachabilityErrors, err.Message)
				}
			}

			if tt.wantErr {
				if len(reachabilityErrors) == 0 {
					t.Errorf("expected reachability error containing %q, got none", tt.errContains)
					return
				}
				errStr := strings.Join(reachabilityErrors, "; ")
				if !strings.Contains(errStr, tt.errContains) {
					t.Errorf("expected error containing %q, got: %s", tt.errContains, errStr)
				}
			} else {
				if len(reachabilityErrors) > 0 {
					t.Errorf("unexpected reachability errors: %v", reachabilityErrors)
				}
			}
		})
	}
}

// =============================================================================
// CYCLE DETECTION TESTS
// =============================================================================

func TestCycleDetection(t *testing.T) {
	tests := []struct {
		name        string
		workflow    *reliantv1.Workflow
		wantErr     bool
		errContains string
	}{
		{
			name: "no cycle - linear",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
					callLLMNode("c"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b"),
					defaultEdge("b", "c"),
				},
			},
			wantErr: false,
		},
		{
			name: "no cycle - diamond",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
					callLLMNode("c"),
					callLLMNode("d"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b", "c"),
					defaultEdge("b", "d"),
					defaultEdge("c", "d"),
				},
			},
			wantErr: false,
		},
		{
			name: "self-loop cycle",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "a"),
				},
			},
			wantErr:     true,
			errContains: "cycle detected: a -> a",
		},
		{
			name: "simple cycle - two nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b"),
					defaultEdge("b", "a"),
				},
			},
			wantErr:     true,
			errContains: "cycle detected: a -> b -> a",
		},
		{
			name: "cycle - three nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
					callLLMNode("c"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b"),
					defaultEdge("b", "c"),
					defaultEdge("c", "a"),
				},
			},
			wantErr:     true,
			errContains: "cycle detected: a -> b -> c -> a",
		},
		{
			name: "cycle via conditional edge",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b"),
					casesEdge("b",
						[]*reliantv1.EdgeCase{
							{Condition: "some_condition", To: []string{"a"}},
						},
					),
				},
			},
			wantErr:     true,
			errContains: "cycle detected",
		},
		{
			name: "cycle not reachable from entry - should not be detected",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"start"},
				Nodes: []*reliantv1.Node{
					callLLMNode("start"),
					callLLMNode("a"),
					callLLMNode("b"),
				},
				Edges: []*reliantv1.Edge{
					// a -> b -> a forms a cycle but it's not reachable from start
					defaultEdge("a", "b"),
					defaultEdge("b", "a"),
				},
			},
			wantErr: false, // Cycle exists but not reachable from entry
		},
		{
			name: "cycle at end of long path",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"a"},
				Nodes: []*reliantv1.Node{
					callLLMNode("a"),
					callLLMNode("b"),
					callLLMNode("c"),
					callLLMNode("d"),
				},
				Edges: []*reliantv1.Edge{
					defaultEdge("a", "b"),
					defaultEdge("b", "c"),
					defaultEdge("c", "d"),
					defaultEdge("d", "c"), // cycle c -> d -> c
				},
			},
			wantErr:     true,
			errContains: "cycle detected: c -> d -> c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StaticAnalysis(tt.workflow, nil)

			if tt.wantErr {
				// Check for cycle-specific errors
				var cycleErrors []string
				for _, err := range result.Errors() {
					if strings.Contains(err.Message, "cycle detected") {
						cycleErrors = append(cycleErrors, err.Message)
					}
				}
				if len(cycleErrors) == 0 {
					t.Errorf("expected cycle error containing %q, got no cycle errors", tt.errContains)
					return
				}
				found := false
				for _, errMsg := range cycleErrors {
					if strings.Contains(errMsg, tt.errContains) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected cycle error containing %q, got: %v", tt.errContains, cycleErrors)
				}
			} else {
				// Ensure no cycle errors
				for _, err := range result.Errors() {
					if strings.Contains(err.Message, "cycle detected") {
						t.Errorf("unexpected cycle error: %s", err.Message)
					}
				}
			}
		})
	}
}

// =============================================================================
// INLINE WORKFLOW TESTS
// =============================================================================

func TestRouterNodeValidation(t *testing.T) {
	tests := []struct {
		name        string
		workflow    *reliantv1.Workflow
		wantErr     bool
		errContains string
	}{
		// === Workflow routing mode (existing) ===
		{
			name: "valid router node with workflows",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Workflows: []*reliantv1.RouterWorkflowCandidate{
									{Ref: "builtin://agent", Presets: []string{"general"}},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "router with neither workflows nor nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "workflows",
		},
		{
			name: "router with both workflows and nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route", "target"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Workflows: []*reliantv1.RouterWorkflowCandidate{
									{Ref: "builtin://agent"},
								},
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "target"},
								},
							},
						},
					},
					{Id: "target", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
				},
			},
			wantErr:     true,
			errContains: "cannot have both",
		},
		{
			name: "router candidate with empty ref",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Workflows: []*reliantv1.RouterWorkflowCandidate{
									{Ref: "", Presets: []string{"general"}},
								},
							},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "ref",
		},
		// === Node routing mode (new) ===
		{
			name: "valid router node with nodes",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route", "target_a", "target_b"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "target_a", Description: "handle A"},
									{Id: "target_b", Description: "handle B"},
								},
							},
						},
					},
					{Id: "target_a", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
					{Id: "target_b", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
				},
			},
			wantErr: false,
		},
		{
			name: "node routing candidate with empty id",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "", Description: "no id"},
								},
							},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "empty id",
		},
		{
			name: "node routing candidate references unknown node",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "nonexistent"},
								},
							},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "unknown node",
		},
		{
			name: "node routing candidate references itself",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "route"},
								},
							},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "itself",
		},
		{
			name: "node routing valid fallback",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route", "target_a", "target_b"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "target_a"},
									{Id: "target_b"},
								},
								Fallback: "target_a",
							},
						},
					},
					{Id: "target_a", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
					{Id: "target_b", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
				},
			},
			wantErr: false,
		},
		{
			name: "node routing fallback not in candidates",
			workflow: &reliantv1.Workflow{
				Name:  "test",
				Entry: []string{"route", "target_a", "other"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "target_a"},
								},
								Fallback: "other",
							},
						},
					},
					{Id: "target_a", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
					{Id: "other", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}},
				},
			},
			wantErr:     true,
			errContains: "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StaticAnalysis(tt.workflow, nil)

			var routerErrors []string
			for _, err := range result.Errors() {
				if tt.errContains != "" && strings.Contains(strings.ToLower(err.Message), tt.errContains) {
					routerErrors = append(routerErrors, err.Message)
				}
			}

			if tt.wantErr {
				if len(routerErrors) == 0 {
					allErrors := result.Errors()
					errMsgs := make([]string, len(allErrors))
					for i, e := range allErrors {
						errMsgs[i] = e.Message
					}
					t.Errorf("expected router error containing %q, got errors: %v", tt.errContains, errMsgs)
				}
			} else {
				// For valid case, check there are no errors at all
				allErrors := result.Errors()
				if len(allErrors) > 0 {
					errMsgs := make([]string, len(allErrors))
					for i, e := range allErrors {
						errMsgs[i] = e.Message
					}
					t.Errorf("expected no errors for valid router, got: %v", errMsgs)
				}
			}
		})
	}
}

func TestInlineWorkflowCycleDetection(t *testing.T) {
	// Test that cycle detection also works for inline workflows
	workflow := &reliantv1.Workflow{
		Name:  "parent",
		Entry: []string{"wf_node"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "wf_node",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Inline: &reliantv1.Workflow{
							Entry: []string{"inner_a"},
							Nodes: []*reliantv1.Node{
								callLLMNode("inner_a"),
								callLLMNode("inner_b"),
							},
							Edges: []*reliantv1.Edge{
								defaultEdge("inner_a", "inner_b"),
								defaultEdge("inner_b", "inner_a"), // cycle
							},
						},
					},
				},
			},
		},
	}

	result := StaticAnalysis(workflow, nil)

	var cycleErrors []string
	for _, err := range result.Errors() {
		if strings.Contains(err.Message, "cycle detected") {
			cycleErrors = append(cycleErrors, err.Message)
		}
	}

	if len(cycleErrors) == 0 {
		t.Error("expected cycle detection in inline workflow, got no cycle errors")
	}
}

func TestInlineWorkflowUnreachableNodeDetection(t *testing.T) {
	// Test that unreachable node detection also works for inline workflows
	workflow := &reliantv1.Workflow{
		Name:  "parent",
		Entry: []string{"wf_node"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "wf_node",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Inline: &reliantv1.Workflow{
							Entry: []string{"inner_a"},
							Nodes: []*reliantv1.Node{
								callLLMNode("inner_a"),
								callLLMNode("inner_orphan"),
							},
							Edges: []*reliantv1.Edge{},
						},
					},
				},
			},
		},
	}

	result := StaticAnalysis(workflow, nil)

	var reachabilityErrors []string
	for _, err := range result.Errors() {
		if strings.Contains(err.Message, "unreachable") {
			reachabilityErrors = append(reachabilityErrors, err.Message)
		}
	}

	if len(reachabilityErrors) == 0 {
		t.Error("expected unreachable node detection in inline workflow, got no errors")
	}
}
