// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestValidateInputsAgainstSchema_PresetAware(t *testing.T) {
	// Helper to create a simple input schema
	makeSchema := func(inputs map[string]*reliantv1.Input) map[string]*reliantv1.Input {
		return inputs
	}

	// Helper preset loader that returns configured presets
	makePresetLoader := func(presets map[string]map[string]interface{}) PresetLoader {
		return func(name string) (map[string]interface{}, error) {
			if params, ok := presets[name]; ok {
				return params, nil
			}
			return nil, fmt.Errorf("preset not found: %s", name)
		}
	}

	tests := []struct {
		name          string
		provided      map[string]interface{}
		presets       map[string]string
		schema        map[string]*reliantv1.Input
		presetLoader  PresetLoader
		wantErrors    bool
		errorContains string
	}{
		{
			name:     "no presets, all args provided",
			provided: map[string]interface{}{"model": "claude-4"},
			presets:  nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader: nil,
			wantErrors:   false,
		},
		{
			name:     "preset provides required input",
			provided: map[string]interface{}{},
			presets:  map[string]string{"default": "general"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{
				"general": {"model": "claude-4-sonnet"},
			}),
			wantErrors: false,
		},
		{
			name:     "args override preset",
			provided: map[string]interface{}{"model": "claude-4-opus"},
			presets:  map[string]string{"default": "general"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{
				"general": {"model": "claude-4-sonnet"},
			}),
			wantErrors: false,
		},
		{
			name:     "missing preset fails validation",
			provided: map[string]interface{}{},
			presets:  map[string]string{"default": "nonexistent"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader:  makePresetLoader(map[string]map[string]interface{}{}),
			wantErrors:    true,
			errorContains: "failed to load preset 'nonexistent'",
		},
		{
			name: "map args validated structurally against group input",
			provided: map[string]interface{}{
				"implementer": map[string]interface{}{
					"model":   "claude-4",
					"unknown": "bad",
				},
			},
			presets: nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"implementer": groupInput(map[string]*reliantv1.Input{
					"model": stringInput(nil),
				}),
			}),
			presetLoader:  nil,
			wantErrors:    true,
			errorContains: "unknown input(s): implementer.unknown",
		},
		{
			name: "map args with no matching group are unknown",
			provided: map[string]interface{}{
				"implementer": map[string]interface{}{
					"model": "claude-4",
				},
			},
			presets: nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader:  nil,
			wantErrors:    true,
			errorContains: "unknown input(s): implementer",
		},
		{
			name:     "unknown args fail validation",
			provided: map[string]interface{}{"unknown_arg": "value"},
			presets:  nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader:  nil,
			wantErrors:    true,
			errorContains: "unknown input(s): unknown_arg",
		},
		{
			name:     "grouped preset applies structurally",
			provided: map[string]interface{}{},
			presets:  map[string]string{"implementer": "general"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"implementer": groupInput(map[string]*reliantv1.Input{
					"model": stringInput(nil),
				}),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{
				"general": {"model": "claude-4-sonnet"},
			}),
			wantErrors: false,
		},
		{
			name:     "grouped preset with required nested input satisfied",
			provided: map[string]interface{}{},
			presets:  map[string]string{"implementer": "general"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"implementer": groupInput(map[string]*reliantv1.Input{
					"model": stringInput(nil),
				}),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{
				"general": {"model": "claude-4-sonnet"},
			}),
			wantErrors: false,
		},
		{
			name:     "missing required nested group input",
			provided: map[string]interface{}{},
			presets:  nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"implementer": groupInput(map[string]*reliantv1.Input{
					"model": stringInput(nil),
				}),
			}),
			presetLoader:  nil,
			wantErrors:    true,
			errorContains: "missing required input(s): implementer.model",
		},
		{
			name: "args override preset within group",
			provided: map[string]interface{}{
				"implementer": map[string]interface{}{
					"model": "claude-4-opus",
				},
			},
			presets: map[string]string{"implementer": "general"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"implementer": groupInput(map[string]*reliantv1.Input{
					"model": stringInput(nil),
				}),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{
				"general": {"model": "claude-4-sonnet"},
			}),
			wantErrors: false,
		},
		{
			name:     "CEL template args are skipped",
			provided: map[string]interface{}{"model": "{{inputs.model}}"},
			presets:  nil,
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(nil),
			}),
			presetLoader: nil,
			wantErrors:   false,
		},
		{
			name:     "CEL template preset name is skipped",
			provided: map[string]interface{}{},
			presets:  map[string]string{"default": "{{inputs.preset}}"},
			schema: makeSchema(map[string]*reliantv1.Input{
				"model": stringInput(ptr.Of("default-model")),
			}),
			presetLoader: makePresetLoader(map[string]map[string]interface{}{}),
			wantErrors:   false, // CEL template preset is skipped, schema has default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewResult()
			validateProtoInputsAgainstSchema(
				[]string{"test", "node"},
				tt.provided,
				tt.presets,
				tt.schema,
				tt.presetLoader,
				nil, // no parent workflow for these unit tests
				result,
			)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" {
				errStr := result.Error()
				if errStr == "" || !containsString(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

func TestStaticAnalysisWithOptions_InlineInputInheritanceContract(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "builtin://parent",
		Inputs: map[string]*reliantv1.Input{
			"model": stringInput(nil),
		},
		Entry: []string{"run-inline"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "run-inline",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Inline: &reliantv1.Workflow{
						Name:  "inline-child",
						Entry: []string{"step"},
						Nodes: []*reliantv1.Node{{
							Id:   "step",
							Type: "call_llm",
							Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}},
						}},
						Inputs: map[string]*reliantv1.Input{
							"child_only": stringInput(nil),
						},
					},
				}},
			},
		},
	}

	result := StaticAnalysisWithOptions(workflow, &ValidationOptions{})
	if result.HasErrors() {
		t.Fatalf("unexpected validation errors for inline inheritance contract: %s", result.Error())
	}
}

func TestStaticAnalysisWithOptions_WorkflowNameIdentityContract(t *testing.T) {
	parent := &reliantv1.Workflow{
		Name:  "builtin://agent",
		Entry: []string{"workflow_call"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "workflow_call",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Inline: &reliantv1.Workflow{
					Name:  "inline::workflow",
					Entry: []string{"inner"},
					Nodes: []*reliantv1.Node{{Id: "inner", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}}},
				}}},
			},
			{
				Id:   "loop_call",
				Type: "loop",
				Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
					While: &reliantv1.DirectCelBool{Expr: "true"},
					Inline: &reliantv1.Workflow{
						Name:  "inline::loop",
						Entry: []string{"inner"},
						Nodes: []*reliantv1.Node{{Id: "inner", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}}},
					},
				}},
			},
		},
		Edges: []*reliantv1.Edge{{From: "workflow_call", Default: []string{"loop_call"}}},
	}

	loader := func(ref string) (*reliantv1.Workflow, error) {
		if ref == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent", Entry: []string{"step"}, Nodes: []*reliantv1.Node{{Id: "step", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}}}}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", ref)
	}

	result := StaticAnalysisWithOptions(parent, &ValidationOptions{WorkflowLoader: loader})
	if result.HasErrors() {
		t.Fatalf("unexpected errors for workflow.name identity contract: %s", result.Error())
	}
}

func TestStaticAnalysisWithOptions_SpawnWorkflowNameIdentityTemplateContract(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name:  "builtin://agent",
		Entry: []string{"llm"},
		Nodes: []*reliantv1.Node{{
			Id:   "llm",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}},
				ToolFilter: &reliantv1.CelStringList{
					Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"{{inputs.tools + [spawn(workflow.name, inputs.spawn_presets)]}}"}}},
				},
			}},
		}},
	}

	t.Run("loadable identity passes", func(t *testing.T) {
		loader := func(ref string) (*reliantv1.Workflow, error) {
			if ref == "builtin://agent" {
				return &reliantv1.Workflow{Name: "agent", Entry: []string{"step"}, Nodes: []*reliantv1.Node{{Id: "step", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}}}}, nil
			}
			return nil, fmt.Errorf("workflow not found: %s", ref)
		}
		result := StaticAnalysisWithOptions(workflow, &ValidationOptions{WorkflowLoader: loader})
		if result.HasErrors() {
			t.Fatalf("unexpected errors: %s", result.Error())
		}
	})

	t.Run("non-loadable identity fails", func(t *testing.T) {
		loader := func(ref string) (*reliantv1.Workflow, error) {
			return nil, fmt.Errorf("workflow not found: %s", ref)
		}
		result := StaticAnalysisWithOptions(workflow, &ValidationOptions{WorkflowLoader: loader})
		if !result.HasErrors() {
			t.Fatalf("expected error for non-loadable workflow.name identity")
		}
		if !strings.Contains(result.Error(), "spawn(workflow.name, ...)") {
			t.Fatalf("expected spawn(workflow.name, ...) error, got: %s", result.Error())
		}
	})
}

func TestStaticAnalysisWithOptions_SpawnWorkflowNameUsesCanonicalWorkflowRefOverride(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name:  "agent",
		Entry: []string{"llm"},
		Nodes: []*reliantv1.Node{{
			Id:   "llm",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}},
				ToolFilter: &reliantv1.CelStringList{
					Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"{{inputs.tools + [spawn(workflow.name, inputs.spawn_presets)]}}"}}},
				},
			}},
		}},
	}

	loader := func(ref string) (*reliantv1.Workflow, error) {
		if ref == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent", Entry: []string{"step"}, Nodes: []*reliantv1.Node{{Id: "step", Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}}}}}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", ref)
	}

	result := StaticAnalysisWithOptions(workflow, &ValidationOptions{
		WorkflowLoader:       loader,
		CanonicalWorkflowRef: "builtin://agent",
	})
	if result.HasErrors() {
		t.Fatalf("expected canonical workflow ref override to satisfy workflow.name loadability, got: %s", result.Error())
	}
}

func TestStaticAnalysisWithOptions_PresetValidation(t *testing.T) {
	// Test that StaticAnalysisWithOptions passes preset loader through to cross-workflow validation

	// Create a parent workflow that references a child workflow
	parentWf := &reliantv1.Workflow{
		Name:  "parent",
		Entry: []string{"call-child"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "call-child",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Ref:     &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "child"}},
					Args:    map[string]*structpb.Value{},
					Presets: map[string]string{"default": "my-preset"},
				}},
			},
		},
	}

	// Create child workflow with required input
	childWf := &reliantv1.Workflow{
		Name:  "child",
		Entry: []string{"step"},
		Inputs: map[string]*reliantv1.Input{
			"required_input": stringInput(nil),
		},
		Nodes: []*reliantv1.Node{
			{
				Id:   "step",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"}}}},
			},
		},
	}

	// Workflow loader returns child workflow
	workflowLoader := func(ref string) (*reliantv1.Workflow, error) {
		if ref == "child" {
			return childWf, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", ref)
	}

	t.Run("without preset loader - missing required input", func(t *testing.T) {
		opts := &ValidationOptions{
			WorkflowLoader: workflowLoader,
			// No preset loader
		}
		result := StaticAnalysisWithOptions(parentWf, opts)
		if !result.HasErrors() {
			t.Errorf("expected error for missing required input")
		}
		if !containsString(result.Error(), "missing required input") {
			t.Errorf("expected 'missing required input' error, got: %s", result.Error())
		}
	})

	t.Run("with preset loader - preset provides required input", func(t *testing.T) {
		opts := &ValidationOptions{
			WorkflowLoader: workflowLoader,
			PresetLoader: func(name string) (map[string]interface{}, error) {
				if name == "my-preset" {
					return map[string]interface{}{"required_input": "value"}, nil
				}
				return nil, fmt.Errorf("preset not found: %s", name)
			},
		}
		result := StaticAnalysisWithOptions(parentWf, opts)
		if result.HasErrors() {
			t.Errorf("unexpected errors: %s", result.Error())
		}
	})

	t.Run("with preset loader - preset not found", func(t *testing.T) {
		opts := &ValidationOptions{
			WorkflowLoader: workflowLoader,
			PresetLoader: func(name string) (map[string]interface{}, error) {
				return nil, fmt.Errorf("preset not found: %s", name)
			},
		}
		result := StaticAnalysisWithOptions(parentWf, opts)
		if !result.HasErrors() {
			t.Errorf("expected error for missing preset")
		}
		if !containsString(result.Error(), "failed to load preset") {
			t.Errorf("expected 'failed to load preset' error, got: %s", result.Error())
		}
	})
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
