// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// TestAgentWorkflowValidation verifies that agent.yaml parses and validates correctly.
func TestAgentWorkflowValidation(t *testing.T) {
	t.Parallel()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		t.Fatalf("Failed to read agent.yaml: %v", err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		t.Fatalf("Failed to parse agent.yaml: %v", err)
	}

	if wf.GetName() != "agent" {
		t.Errorf("Expected workflow name 'agent', got '%s'", wf.GetName())
	}

	// Verify nodes exist
	nodeIDs := make(map[string]bool)
	for _, node := range wf.GetNodes() {
		nodeIDs[node.GetId()] = true
	}

	expectedNodes := []string{"agent_loop"}
	for _, id := range expectedNodes {
		if !nodeIDs[id] {
			t.Errorf("Expected node '%s' not found", id)
		}
	}

	// Verify agent_loop has loop configuration with inline body
	for _, node := range wf.GetNodes() {
		if node.GetId() == "agent_loop" {
			if node.GetType() != "loop" {
				t.Errorf("agent_loop should have type 'loop', got %q", node.GetType())
			} else {
				loopArgs := model.GetLoopArgs(node)
				if loopArgs == nil {
					t.Error("Expected agent_loop to have loop args")
				} else {
					// Should have inline body, not workflow reference
					if loopArgs.GetInline() == nil {
						t.Error("Expected agent_loop to have inline body")
					} else if len(loopArgs.GetInline().GetNodes()) == 0 {
						t.Error("Expected agent_loop inline body to have steps")
					}
				}
			}
		}
	}

	// Verify outputs are declared
	// Note: iterations and succeeded were removed in the simplified loop model
	expectedOutputs := []string{"message", "response_text"}
	for _, name := range expectedOutputs {
		if _, ok := wf.GetOutputs()[name]; !ok {
			t.Errorf("Expected output '%s' not declared", name)
		}
	}
}

// TestCompactWorkflowValidation verifies that the hardcoded compact workflow is valid.
func TestCompactWorkflowValidation(t *testing.T) {
	t.Parallel()
	// Compact is now a hardcoded internal workflow YAML, not a YAML file
	data := builtin.GetInternalWorkflowYAML("compact")
	if data == nil {
		t.Fatal("Failed to get compact workflow YAML from internal workflows")
	}

	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		t.Fatalf("Failed to parse compact workflow: %v", err)
	}

	if wf.GetName() != "compact" {
		t.Errorf("Expected workflow name 'compact', got '%s'", wf.GetName())
	}

	// Verify compact node exists
	var compactNode bool
	for _, node := range wf.GetNodes() {
		if node.GetId() == "compact" {
			compactNode = true
			if node.GetType() != "compact" {
				t.Errorf("Expected compact node type 'compact', got '%s'", node.GetType())
			}
		}
	}

	if !compactNode {
		t.Error("Expected compact node not found")
	}

	// Validate the workflow
	result := validation.StaticAnalysis(wf, nil)
	if result.AsError() != nil {
		t.Errorf("Compact workflow validation failed: %v", result.AsError())
	}
}

// TestAgentSpawnPresets verifies that the spawn_presets input is correctly configured.
func TestAgentSpawnPresets(t *testing.T) {
	t.Parallel()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		t.Fatalf("Failed to read agent.yaml: %v", err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		t.Fatalf("Failed to parse agent.yaml: %v", err)
	}

	// Verify spawn_presets input exists (preset type for spawn tool configuration)
	spawnPresetsInput, ok := wf.GetInputs()["spawn_presets"]
	if !ok {
		t.Fatal("Expected 'spawn_presets' input not found")
	}

	// Check type is preset with multi=true
	if spawnPresetsInput.GetType() != "preset" {
		t.Errorf("Expected spawn_presets type 'preset', got '%s'", spawnPresetsInput.GetType())
	}
	presetConfig := spawnPresetsInput.GetPresetInput()
	if presetConfig == nil {
		t.Fatalf("Expected spawn_presets to have PresetInputConfig, got nil")
	}
	if !presetConfig.GetMulti() {
		t.Error("Expected spawn_presets to have multi=true")
	}

	// Check tags - should filter by 'agent' tag
	if len(presetConfig.GetTags()) != 1 || presetConfig.GetTags()[0] != "agent" {
		t.Errorf("Expected spawn_presets tags to be [agent], got %v", presetConfig.GetTags())
	}

	// Check default presets (general, researcher, code_reviewer)
	defaultVal := presetConfig.GetDefault()
	if defaultVal == nil {
		t.Fatal("Expected default value for spawn_presets, got nil")
	}
	defaultList := defaultVal.GetListValue()
	if defaultList == nil {
		t.Fatalf("Expected default to be a list, got %T", defaultVal.GetKind())
	}
	if len(defaultList.GetValues()) != 3 {
		t.Errorf("Expected 3 default presets, got %d: %v", len(defaultList.GetValues()), defaultList.GetValues())
	}
}

// TestParseBuiltinWorkflow tests loading builtin workflows by name.
func TestParseBuiltinWorkflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		workflowName string
		expectedName string
		expectError  bool
	}{
		{
			name:         "agent workflow",
			workflowName: "agent",
			expectedName: "agent",
			expectError:  false,
		},
		{
			name:         "compact workflow (internal)",
			workflowName: "compact",
			expectedName: "compact",
			expectError:  false,
		},
		{
			name:         "non-existent builtin",
			workflowName: "nonexistent",
			expectedName: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First check internal (hardcoded) workflows
			if yamlData := builtin.GetInternalWorkflowYAML(tt.workflowName); yamlData != nil {
				wf, err := wfyaml.ParseWorkflow(yamlData)
				if err != nil {
					if tt.expectError {
						return
					}
					t.Fatalf("Failed to parse internal workflow: %v", err)
				}
				if tt.expectError {
					t.Error("Expected error but got workflow")
					return
				}
				if wf.GetName() != tt.expectedName {
					t.Errorf("Expected workflow name '%s', got '%s'", tt.expectedName, wf.GetName())
				}
				return
			}

			// Then try YAML-based builtins
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(tt.workflowName + ".yaml")
			if err != nil {
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("Failed to read builtin workflow: %v", err)
			}

			wf, err := v2.ParseWorkflowProtoBytes(data)
			if err != nil {
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("Unexpected parse error: %v", err)
			}

			if tt.expectError {
				t.Error("Expected error but got nil")
				return
			}

			if wf.GetName() != tt.expectedName {
				t.Errorf("Expected workflow name '%s', got '%s'", tt.expectedName, wf.GetName())
			}
		})
	}
}
