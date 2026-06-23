// Copyright (c) 2025 Reliant Labs
package core

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestCompileSubWorkflowSemanticContracts(t *testing.T) {
	tests := []struct {
		name     string
		workflow *reliantv1.Workflow
		options  CompileOptions
		assert   func(t *testing.T, program *Program, err error)
	}{
		{
			name: "inline workflow inherits parent identity and input policy",
			workflow: &reliantv1.Workflow{
				Name: "workflow-name-not-used-for-identity",
				Nodes: []*reliantv1.Node{
					{
						Id:   "plan",
						Type: "workflow",
						Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
							Inline: &reliantv1.Workflow{Name: "inline-plan"},
						}},
					},
				},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://agent"},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				contract, ok := program.Semantics.SubWorkflows["plan"]
				if !ok {
					t.Fatalf("expected contract for node path plan")
				}
				if contract.WorkflowIdentity != "builtin://agent" {
					t.Fatalf("workflow identity mismatch: got %q", contract.WorkflowIdentity)
				}
				if contract.InputPolicy != InputPolicyInlineInheritParentInputs {
					t.Fatalf("input policy mismatch: got %q", contract.InputPolicy)
				}
				if contract.LoadStrategy != LoadStrategyInlineEmbedded {
					t.Fatalf("load strategy mismatch: got %q", contract.LoadStrategy)
				}
				if len(contract.InputAssembly) != 1 || contract.InputAssembly[0] != InputAssemblyStageInheritParentInputs {
					t.Fatalf("unexpected input assembly: %#v", contract.InputAssembly)
				}
			},
		},
		{
			name: "ref workflow uses workflow ref identity and presets args defaults policy",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{
					{
						Id:   "child",
						Type: "workflow",
						Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
							Ref: celString("builtin://one-ring"),
							Args: map[string]*structpb.Value{
								"model":   structpb.NewStringValue("gpt-5"),
								"retries": structpb.NewNumberValue(2),
							},
							Presets: map[string]string{"default": "baseline", "reviewer": "strict"},
						}},
					},
				},
			},
			options: CompileOptions{
				CanonicalWorkflowRef: "builtin://agent",
				WorkflowLoader: func(workflowRef string) (*reliantv1.Workflow, error) {
					if workflowRef != "builtin://one-ring" {
						t.Fatalf("unexpected workflow ref %q", workflowRef)
					}
					return &reliantv1.Workflow{
						Name: "one-ring",
						Inputs: map[string]*reliantv1.Input{
							"model": stringInputWithDefault("claude-sonnet"),
							"reviewer": groupInput(map[string]*reliantv1.Input{
								"temperature": numberInputWithDefault(0.2),
							}),
						},
					}, nil
				},
			},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				contract := program.Semantics.SubWorkflows["child"]
				if contract.WorkflowIdentity != "builtin://one-ring" {
					t.Fatalf("workflow identity mismatch: got %q", contract.WorkflowIdentity)
				}
				if contract.InputPolicy != InputPolicyRefPresetsArgsDefaults {
					t.Fatalf("input policy mismatch: got %q", contract.InputPolicy)
				}
				if contract.LoadStrategy != LoadStrategyLoadByWorkflowRef {
					t.Fatalf("load strategy mismatch: got %q", contract.LoadStrategy)
				}
				if len(contract.InputAssembly) != 4 ||
					contract.InputAssembly[0] != InputAssemblyStagePresets ||
					contract.InputAssembly[1] != InputAssemblyStagePassthrough ||
					contract.InputAssembly[2] != InputAssemblyStageArgs ||
					contract.InputAssembly[3] != InputAssemblyStageDefaults {
					t.Fatalf("unexpected input assembly: %#v", contract.InputAssembly)
				}
				if contract.Presets["default"] != "baseline" || contract.Presets["reviewer"] != "strict" {
					t.Fatalf("presets not preserved: %#v", contract.Presets)
				}
				if contract.Args["model"] != "gpt-5" {
					t.Fatalf("args not converted: %#v", contract.Args)
				}
				defaults := contract.DefaultInputs
				if defaults["model"] != "claude-sonnet" {
					t.Fatalf("model default mismatch: %#v", defaults)
				}
				reviewer, ok := defaults["reviewer"].(map[string]any)
				if !ok {
					t.Fatalf("reviewer defaults missing: %#v", defaults)
				}
				if reviewer["temperature"] != 0.2 {
					t.Fatalf("reviewer.temperature default mismatch: %#v", reviewer)
				}
			},
		},
		{
			name: "router node uses first candidate ref as placeholder identity",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Workflows: []*reliantv1.RouterWorkflowCandidate{
									{Ref: "builtin://agent", Presets: []string{"general", "researcher"}},
									{Ref: "builtin://code-review"},
								},
							},
						},
					},
				},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://root"},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				contract, ok := program.Semantics.SubWorkflows["route"]
				if !ok {
					t.Fatalf("expected contract for node path route")
				}
				if contract.NodeType != "router" {
					t.Fatalf("node type mismatch: got %q", contract.NodeType)
				}
				if contract.WorkflowRef != "builtin://agent" {
					t.Fatalf("workflow ref mismatch: got %q, want %q", contract.WorkflowRef, "builtin://agent")
				}
				if contract.InvocationMode != InvocationModeRef {
					t.Fatalf("invocation mode mismatch: got %q", contract.InvocationMode)
				}
				if contract.LoadStrategy != LoadStrategyLoadByWorkflowRef {
					t.Fatalf("load strategy mismatch: got %q", contract.LoadStrategy)
				}
				if contract.InputPolicy != InputPolicyRefPresetsArgsDefaults {
					t.Fatalf("input policy mismatch: got %q", contract.InputPolicy)
				}
			},
		},
		{
			name: "router node with no workflow candidates produces no sub-workflow contract",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Workflows: []*reliantv1.RouterWorkflowCandidate{},
							},
						},
					},
				},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://root"},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				// Empty workflow candidates means no child workflow to load
				if _, ok := program.Semantics.SubWorkflows["route"]; ok {
					t.Fatalf("router with no workflow candidates should not produce a sub-workflow contract")
				}
			},
		},
		{
			name: "node routing router produces no sub-workflow contract",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "summarize", Description: "Summarize content"},
									{Id: "translate", Description: "Translate text"},
								},
							},
						},
					},
					{Id: "summarize", Type: "call_llm"},
					{Id: "translate", Type: "call_llm"},
				},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://root"},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				// Node routing routers dispatch to sibling nodes, not child workflows.
				// They should NOT produce sub-workflow contracts.
				if _, ok := program.Semantics.SubWorkflows["route"]; ok {
					t.Fatalf("node routing router should not produce a sub-workflow contract")
				}
			},
		},
		{
			name: "node routing router with fallback compiles without error",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{
					{
						Id:   "route",
						Type: "router",
						Args: &reliantv1.Node_Router{
							Router: &reliantv1.RouterArgs{
								Nodes: []*reliantv1.NodeRouterCandidate{
									{Id: "summarize"},
									{Id: "translate"},
								},
								Fallback: "summarize",
							},
						},
					},
					{Id: "summarize", Type: "call_llm"},
					{Id: "translate", Type: "call_llm"},
				},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://root"},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				// Should still compile fine; no sub-workflow contract expected
				if _, ok := program.Semantics.SubWorkflows["route"]; ok {
					t.Fatalf("node routing router should not produce a sub-workflow contract")
				}
			},
		},
		{
			name: "nested inline under ref inherits referenced workflow identity",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{{
					Id:   "parent_ref",
					Type: "workflow",
					Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Ref: celString("builtin://outer")}},
				}},
			},
			options: CompileOptions{
				CanonicalWorkflowRef: "builtin://agent",
				WorkflowLoader: func(workflowRef string) (*reliantv1.Workflow, error) {
					if workflowRef != "builtin://outer" {
						t.Fatalf("unexpected workflow ref %q", workflowRef)
					}
					return &reliantv1.Workflow{
						Name: "outer",
						Nodes: []*reliantv1.Node{{
							Id:   "inline_inner",
							Type: "workflow",
							Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
								Inline: &reliantv1.Workflow{Name: "inner"},
							}},
						}},
					}, nil
				},
			},
			assert: func(t *testing.T, program *Program, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("compile returned error: %v", err)
				}
				outer := program.Semantics.SubWorkflows["parent_ref"]
				if outer.WorkflowIdentity != "builtin://outer" {
					t.Fatalf("outer identity mismatch: %q", outer.WorkflowIdentity)
				}
				inner := program.Semantics.SubWorkflows["parent_ref/inline_inner"]
				if inner.WorkflowIdentity != "builtin://outer" {
					t.Fatalf("inner identity mismatch: %q", inner.WorkflowIdentity)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			program, err := Compile(tt.workflow, tt.options)
			tt.assert(t, program, err)
		})
	}
}

func TestCompileValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		workflow *reliantv1.Workflow
		options  CompileOptions
	}{
		{
			name:     "nil workflow",
			workflow: nil,
		},
		{
			name:     "missing canonical ref and name",
			workflow: &reliantv1.Workflow{},
		},
		{
			name: "synthetic inline ref rejected",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{{
					Id:   "bad_ref",
					Type: "workflow",
					Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Ref: celString("builtin://agent::inline")}},
				}},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://agent"},
		},
		{
			name: "empty ref rejected",
			workflow: &reliantv1.Workflow{
				Name: "root",
				Nodes: []*reliantv1.Node{{
					Id:   "empty_ref",
					Type: "loop",
					Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Ref: celString("  ")}},
				}},
			},
			options: CompileOptions{CanonicalWorkflowRef: "builtin://agent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.workflow, tt.options)
			if err == nil {
				t.Fatalf("expected compile error")
			}
		})
	}
}

func TestCompileUsesWorkflowNameAsFallbackCanonicalRef(t *testing.T) {
	program, err := Compile(&reliantv1.Workflow{
		Name: "builtin://fallback",
		Nodes: []*reliantv1.Node{{
			Id:   "inline",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Inline: &reliantv1.Workflow{Name: "x"}}},
		}},
	}, CompileOptions{})
	if err != nil {
		t.Fatalf("compile returned error: %v", err)
	}

	if program.Semantics.CanonicalWorkflowRef != "builtin://fallback" {
		t.Fatalf("canonical ref mismatch: got %q", program.Semantics.CanonicalWorkflowRef)
	}
	contract := program.Semantics.SubWorkflows["inline"]
	if contract.WorkflowIdentity != "builtin://fallback" {
		t.Fatalf("workflow identity mismatch: got %q", contract.WorkflowIdentity)
	}
}

func celString(value string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: value}}
}

func stringInputWithDefault(defaultValue string) *reliantv1.Input {
	return &reliantv1.Input{
		Type:   "string",
		Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{Default: &defaultValue}},
	}
}

func numberInputWithDefault(defaultValue float64) *reliantv1.Input {
	return &reliantv1.Input{
		Type:   "number",
		Config: &reliantv1.Input_NumberInput{NumberInput: &reliantv1.NumberInputConfig{Default: &defaultValue}},
	}
}

func groupInput(inputs map[string]*reliantv1.Input) *reliantv1.Input {
	return &reliantv1.Input{
		Type:   "group",
		Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{Inputs: inputs}},
	}
}
