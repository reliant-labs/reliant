// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	// Import activities to trigger init() which registers activity schemas.
	// This enables validation of activity input fields (e.g., call_llm args).
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// TestValidateBuiltinWorkflows validates all builtin workflows pass full validation.
// This runs the same validation that the runtime uses, catching issues like:
// - Unknown arg fields (e.g., "ephemeral" on call_llm)
// - Invalid model IDs
// - CEL expression errors
// - Missing required fields
func TestValidateBuiltinWorkflows(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Should be able to read builtin workflows directory")

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
			require.NoError(t, err, "Failed to read %s", entry.Name())

			wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			require.NoError(t, err, "Failed to parse %s", entry.Name())

			// Run FULL validation - same as runtime uses
			// This catches unknown arg fields, invalid CEL, etc.
			result := validation.StaticAnalysis(wf, builtinLoader)
			require.NoError(t, result.AsError(), "Validation failed for %s", entry.Name())

			// Basic sanity checks
			assert.NotEmpty(t, wf.GetName(), "Workflow name is empty")
			assert.NotEmpty(t, wf.GetNodes(), "Workflow has no nodes")
			assert.True(t, len(wf.GetEdges()) > 0 || len(wf.GetEntry()) > 0, "Workflow has neither edges nor entry field")
		})
	}
}

// TestAllBuiltinWorkflowsDiscoverable ensures all .yaml files in builtin/ can be read from embedded FS
func TestAllBuiltinWorkflowsDiscoverable(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Should be able to read builtin workflows directory")

	var yamlFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml") {
			yamlFiles = append(yamlFiles, entry.Name())
		}
	}

	require.NotEmpty(t, yamlFiles, "Should have at least one builtin workflow")
	t.Logf("Found %d builtin workflows: %v", len(yamlFiles), yamlFiles)

	for _, filename := range yamlFiles {
		t.Run(filename, func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
			require.NoError(t, err, "Should be able to read %s", filename)
			require.NotEmpty(t, data, "File %s should not be empty", filename)

			wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			require.NoError(t, err, "Should be able to parse %s", filename)
			require.NotNil(t, wf, "Workflow should not be nil")
			assert.NotEmpty(t, wf.GetName(), "Workflow should have a name")
		})
	}
}

// TestStructuredAgentWorkflowExists tests that structured-agent workflow exists and can be loaded
func TestStructuredAgentWorkflowExists(t *testing.T) {
	// Test that the file exists in embedded FS
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("structured-agent.yaml")
	require.NoError(t, err, "structured-agent.yaml should exist in builtin workflows")
	require.NotEmpty(t, data, "structured-agent.yaml should not be empty")

	// Test that it parses correctly
	wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
	require.NoError(t, err, "structured-agent.yaml should parse without errors")
	require.NotNil(t, wf, "Parsed workflow should not be nil")

	// Verify workflow properties
	assert.Equal(t, "structured-agent", wf.GetName(), "Workflow name should be 'structured-agent'")
	assert.NotEmpty(t, wf.GetNodes(), "Workflow should have nodes")
	// New model: workflows have either edges or entry field
	assert.True(t, len(wf.GetEdges()) > 0 || len(wf.GetEntry()) > 0, "Workflow should have edges or entry field")

	// Verify workflow uses new model with entry field
	assert.True(t, len(wf.GetEntry()) > 0, "Workflow should have explicit entry field (new thread model)")
}

// TestBuiltinWorkflowLoading simulates how the runtime loads builtin workflows
// This tests the same code path that would be used when a user selects builtin://agent
func TestBuiltinWorkflowLoading(t *testing.T) {
	testCases := []string{
		"agent",
		"auditing-agent",
		"compact", // Note: compact is now a hardcoded internal workflow
		"discovery-relay",
		"get-it-right",
		"markdown-checklist",
		"one-ring",
		"parallel-compete",
		"parallel-loop-sample",
		"ralph-wiggum",
		"structured-agent",
	}

	for _, name := range testCases {
		t.Run("builtin://"+name, func(t *testing.T) {
			// Check for internal (hardcoded) workflows first
			if yamlData := builtin.GetInternalWorkflowYAML(name); yamlData != nil {
				wf, err := wfyaml.ParseWorkflow(yamlData)
				require.NoError(t, err, "Should parse internal workflow")
				require.NotNil(t, wf, "Internal workflow should not be nil")
				assert.Equal(t, name, wf.GetName(), "Workflow name should match")
				return
			}

			// This mimics the runtime loading path for YAML-based builtins
			filename := name + ".yaml"
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
			if err != nil {
				t.Fatalf("builtin workflow not found: builtin://%s (tried embedded %s): %v", name, filename, err)
			}

			wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			require.NoError(t, err, "Should parse builtin://%s", name)
			require.NotNil(t, wf, "Workflow should not be nil")
			assert.Equal(t, name, wf.GetName(), "Workflow name should match")
		})
	}
}

// TestBuiltinWorkflowTemplateResolution tests that complex builtin workflows
// can be loaded via ResolveAndParseWorkflow without errors.
// This originally caught a bug where trigger templates inside nodes were being resolved too early.
func TestBuiltinWorkflowTemplateResolution(t *testing.T) {
	// Complex workflows with nested structures and CEL expressions
	workflowsToTest := []string{
		"parallel-compete", // Complex parallel workflow with worktrees
	}

	for _, name := range workflowsToTest {
		t.Run("builtin://"+name, func(t *testing.T) {
			filename := name + ".yaml"
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
			require.NoError(t, err, "Should read builtin workflow: %s", name)

			// ResolveAndParseWorkflow is what the runtime uses
			wf, err := runtime.ResolveAndParseWorkflow(data, map[string]interface{}{})
			require.NoError(t, err, "ResolveAndParseWorkflow should succeed for builtin://%s", name)
			require.NotNil(t, wf, "Workflow should not be nil")
			assert.Equal(t, name, wf.GetName(), "Workflow name should match")
		})
	}
}

// TestBuiltinWorkflowModelIDsAreValid validates that all model defaults in builtin workflows
// reference valid, registered model IDs. This catches mismatches like using "claude-sonnet-4.5"
// when the registered model ID is "claude-4.5-sonnet".
func TestBuiltinWorkflowModelIDsAreValid(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Should be able to read builtin workflows directory")

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
			require.NoError(t, err, "Should be able to read %s", entry.Name())

			wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			require.NoError(t, err, "Should be able to parse %s", entry.Name())

			// Check top-level inputs for model defaults
			validateModelInputs(t, wf.GetInputs(), entry.Name(), "inputs")

			// Check groups for model defaults (groups are inputs with type: group)
			for groupName, input := range wf.GetInputs() {
				if groupConfig := input.GetGroupInput(); groupConfig != nil {
					validateModelInputs(t, groupConfig.GetInputs(), entry.Name(), "inputs."+groupName)
				}
			}

			// Check inline workflows in nodes
			validateNodesForModels(t, wf.GetNodes(), entry.Name(), "nodes")
		})
	}
}

// validateModelInputs checks if any model-type inputs have valid default model IDs
func validateModelInputs(t *testing.T, inputs map[string]*reliantv1.Input, filename, path string) {
	for name, input := range inputs {
		if input.GetType() != "model" {
			continue
		}
		modelConfig := input.GetModelInput()
		if modelConfig == nil || modelConfig.GetDefault() == nil {
			continue
		}

		def := modelConfig.GetDefault()
		// If using tags, can't validate statically
		if len(def.GetTags()) > 0 {
			continue
		}
		if def.GetId() == "" {
			continue
		}
		registry := models.MustGetRegistry()
		if _, found := registry.GetDefinition(def.GetId()); !found {
			t.Errorf("%s: %s.%s has invalid model default %q - model not found in registry",
				filename, path, name, def.GetId())
		}
	}
}

// validateNodesForModels recursively checks nodes for inline workflows with model inputs
func validateNodesForModels(t *testing.T, nodes []*reliantv1.Node, filename, path string) {
	for _, node := range nodes {
		nodePath := path + "." + node.GetId()

		// Check inline workflow inputs — only loop and workflow nodes have inline
		if loopArgs := model.GetLoopArgs(node); loopArgs != nil {
			if loopArgs.GetInline() != nil {
				validateModelInputs(t, loopArgs.GetInline().GetInputs(), filename, nodePath+".inline.inputs")
				validateNodesForModels(t, loopArgs.GetInline().GetNodes(), filename, nodePath+".inline.nodes")
			}
		}
		if subWfArgs := model.GetSubWorkflowArgs(node); subWfArgs != nil {
			if subWfArgs.GetInline() != nil {
				validateModelInputs(t, subWfArgs.GetInline().GetInputs(), filename, nodePath+".inline.inputs")
				validateNodesForModels(t, subWfArgs.GetInline().GetNodes(), filename, nodePath+".inline.nodes")
			}
		}
	}
}

// TestBuiltinPresetsValidateAgainstWorkflows validates that all builtin presets
// are valid when applied to all builtin workflows that have matching tags.
// This catches issues like:
// - Presets with params that don't exist in the target workflow
// - Type mismatches between preset params and workflow inputs
func TestBuiltinPresetsValidateAgainstWorkflows(t *testing.T) {
	// Load all builtin workflows
	var workflows []*reliantv1.Workflow
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		require.NoError(t, err)

		wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
		require.NoError(t, err)
		workflows = append(workflows, wf)
	}

	// Load all builtin presets
	presetEntries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	require.NoError(t, err)
	require.NotEmpty(t, presetEntries, "Should have at least one builtin preset")

	for _, presetEntry := range presetEntries {
		if presetEntry.IsDir() || !strings.HasSuffix(presetEntry.Name(), ".yaml") {
			continue
		}

		t.Run("preset:"+presetEntry.Name(), func(t *testing.T) {
			data, err := builtin.BuiltinPresetsFS.ReadFile("presets/" + presetEntry.Name())
			require.NoError(t, err)

			// Parse preset YAML to get tag
			var presetDef struct {
				Name   string                 `yaml:"name"`
				Tag    string                 `yaml:"tag"`
				Params map[string]interface{} `yaml:"params"`
			}
			err = yaml.Unmarshal(data, &presetDef)
			require.NoError(t, err, "Failed to parse preset %s", presetEntry.Name())

			if presetDef.Tag == "" {
				t.Skip("Preset has no tag, skipping workflow matching")
				return
			}

			// Check against each workflow that has matching tags
			for _, wf := range workflows {
				// Collect valid input names from matching targets
				var validInputs map[string]*reliantv1.Input

				// Check workflow-level tag
				if wf.GetPresets() != nil && wf.GetPresets().GetTag() == presetDef.Tag {
					validInputs = getNonGroupInputs(wf.GetInputs())
				}

				// Check group-level tags
				for _, input := range wf.GetInputs() {
					if groupConfig := input.GetGroupInput(); groupConfig != nil {
						if groupConfig.GetPresets() != nil && groupConfig.GetPresets().GetTag() == presetDef.Tag {
							validInputs = groupConfig.GetInputs()
						}
					}
				}

				if validInputs == nil {
					continue // No matching tag in this workflow
				}

				// Verify each preset param exists in the target
				for paramName := range presetDef.Params {
					if _, exists := validInputs[paramName]; !exists {
						t.Errorf("Preset %s param %q does not exist in workflow %s (available: %v)",
							presetDef.Name, paramName, wf.GetName(), protoInputNames(validInputs))
					}
				}
			}
		})
	}
}

// getNonGroupInputs returns non-group inputs from a proto input map
func getNonGroupInputs(inputs map[string]*reliantv1.Input) map[string]*reliantv1.Input {
	result := make(map[string]*reliantv1.Input)
	for name, input := range inputs {
		if !model.IsGroupInput(input) {
			result[name] = input
		}
	}
	return result
}

// protoInputNames returns the names of all inputs as a slice for error messages
func protoInputNames(inputs map[string]*reliantv1.Input) []string {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	return names
}

// TestBuiltinWorkflowNodePresetReferencesExist validates that all node-level
// preset references (presets.default) in builtin workflows point to presets
// that actually exist. This catches issues like:
// - Typos in preset names (e.g., "reviewer" vs "code_reviewer")
// - References to deleted presets
// - Missing presets that should exist
func TestBuiltinWorkflowNodePresetReferencesExist(t *testing.T) {
	// Load all builtin preset names
	presetEntries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	require.NoError(t, err, "Should be able to read builtin presets directory")

	availablePresets := make(map[string]bool)
	for _, entry := range presetEntries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		// Extract preset name from filename (without .yaml extension)
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		name = strings.TrimSuffix(name, ".yml")
		availablePresets[name] = true
	}
	require.NotEmpty(t, availablePresets, "Should have at least one builtin preset")

	// Load all builtin workflows
	workflowEntries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Should be able to read builtin workflows directory")

	for _, entry := range workflowEntries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
			require.NoError(t, err, "Should be able to read %s", entry.Name())

			wf, err := runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			require.NoError(t, err, "Should be able to parse %s", entry.Name())

			// Check all nodes for preset references
			validateNodePresetRefs(t, wf.GetNodes(), availablePresets, entry.Name(), "nodes")
		})
	}
}

// validateNodePresetRefs recursively checks nodes for preset references
func validateNodePresetRefs(t *testing.T, nodes []*reliantv1.Node, availablePresets map[string]bool, filename, path string) {
	for _, node := range nodes {
		nodePath := path + "." + node.GetId()

		// Check for preset references — only workflow/loop nodes have presets
		if subWfArgs := model.GetSubWorkflowArgs(node); subWfArgs != nil {
			checkSubWorkflowPresets(t, subWfArgs.GetPresets(), availablePresets, filename, nodePath)
			if subWfArgs.GetInline() != nil {
				validateNodePresetRefs(t, subWfArgs.GetInline().GetNodes(), availablePresets, filename, nodePath+".inline.nodes")
			}
		}
		if loopArgs := model.GetLoopArgs(node); loopArgs != nil {
			checkSubWorkflowPresets(t, loopArgs.GetPresets(), availablePresets, filename, nodePath)
			if loopArgs.GetInline() != nil {
				validateNodePresetRefs(t, loopArgs.GetInline().GetNodes(), availablePresets, filename, nodePath+".inline.nodes")
			}
		}
	}
}

// checkSubWorkflowPresets checks preset references on a sub-workflow node
func checkSubWorkflowPresets(t *testing.T, presets map[string]string, availablePresets map[string]bool, filename, nodePath string) {
	for groupKey, presetName := range presets {
		if presetName == "" {
			continue
		}
		// Skip CEL template expressions - can't validate statically
		if strings.Contains(presetName, "{{") {
			continue
		}
		if !availablePresets[presetName] {
			var availableList []string
			for name := range availablePresets {
				availableList = append(availableList, name)
			}
			t.Errorf("%s: %s.presets.%s references preset %q which does not exist.\nAvailable presets: %v",
				filename, nodePath, groupKey, presetName, availableList)
		}
	}
}