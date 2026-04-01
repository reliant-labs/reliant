// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// =============================================================================
// Cross-Workflow Spawn Ref Validation Tests
// =============================================================================

func makeCallLLMNode(id string, toolFilter []string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:   id,
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			ToolFilter: &reliantv1.CelStringList{
				Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: toolFilter}},
			},
		}},
	}
}

func TestValidateSpawnRefsLoadable_ExistingWorkflow(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		if name == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent", Entry: []string{"loop"}, Nodes: []*reliantv1.Node{
				{Id: "loop", Type: "loop", Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{}}},
			}}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	node := makeCallLLMNode("call_llm", []string{"spawn:builtin://agent(general,researcher)"})

	result := NewResult()
	validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

	assert.False(t, result.HasErrors(), "existing workflow should not produce errors: %v", result)
}

func TestValidateSpawnRefsLoadable_NonExistentWorkflow(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	node := makeCallLLMNode("call_llm", []string{"spawn:builtin://nonexistent(general)"})

	result := NewResult()
	validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

	require.True(t, result.HasErrors(), "non-existent workflow should produce an error")
	assert.Contains(t, result.Error(), "not a loadable workflow")
	assert.Contains(t, result.Error(), "builtin://nonexistent")
}

func TestValidateSpawnRefsLoadable_TemplateSpawnWorkflowNameUsesIdentity(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		if name == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent"}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	node := makeCallLLMNode("call_llm", []string{"{{inputs.tools + [spawn(workflow.name, inputs.spawn_presets)]}}"})

	result := NewResult()
	validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

	assert.False(t, result.HasErrors(), "template spawn(workflow.name, ...) should validate via identity: %v", result)
}

func TestValidateSpawnRefsLoadable_TemplateSpawnWorkflowNameExpressionMustBeDirectIdentity(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		if name == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent"}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	tests := []string{
		"{{inputs.tools + [spawn(workflow.name + \"::suffix\", inputs.spawn_presets)]}}",
		"{{inputs.tools + [spawn(workflow.name.trim(), inputs.spawn_presets)]}}",
	}

	for _, filter := range tests {
		filter := filter
		t.Run(filter, func(t *testing.T) {
			node := makeCallLLMNode("call_llm", []string{filter})

			result := NewResult()
			validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

			require.True(t, result.HasErrors(), "non-direct workflow.name expressions must be rejected")
			assert.Contains(t, result.Error(), "must use workflow.name as the direct first argument")
		})
	}
}

func TestValidateSpawnRefsLoadable_TemplateSpawnWorkflowNameWithWhitespaceStillValidates(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		if name == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent"}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	node := makeCallLLMNode("call_llm", []string{"{{inputs.tools + [spawn( workflow.name , inputs.spawn_presets )]}}"})

	result := NewResult()
	validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

	assert.False(t, result.HasErrors(), "whitespace-only formatting changes should preserve direct workflow.name identity contract: %v", result)
}

func TestValidateSpawnRefsLoadable_SyntheticNameIsRejected(t *testing.T) {
	loader := func(name string) (*reliantv1.Workflow, error) {
		if name == "builtin://agent" {
			return &reliantv1.Workflow{Name: "agent"}, nil
		}
		return nil, fmt.Errorf("workflow not found: %s", name)
	}

	node := makeCallLLMNode("call_llm", []string{"spawn:builtin://agent::agent_loop(general)"})

	result := NewResult()
	validateSpawnRefsLoadable(node, loader, []string{"test"}, "builtin://agent", result)

	require.True(t, result.HasErrors(), "synthetic inline name should be rejected")
	assert.Contains(t, result.Error(), "agent::agent_loop")
	assert.Contains(t, result.Error(), "synthetic inline workflow name")
}
