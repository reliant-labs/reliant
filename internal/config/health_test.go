// Copyright (c) 2025 Reliant Labs
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/validation"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

// TestBuiltinPresetsLoadForAgentWorkflow replicates the exact validation
// that happens at runtime when the API calls LoadForWorkflow.
// This is the same code path that populates the config health collector.
func TestBuiltinPresetsLoadForAgentWorkflow(t *testing.T) {
	// Clear the global health collector first
	collector := validation.Global()
	collector.Clear()

	// Load the agent workflow (the primary workflow presets target)
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	require.NoError(t, err, "should read agent workflow")

	wf, err := v2.ParseWorkflowProtoBytes(data)
	require.NoError(t, err, "should parse agent workflow")

	// Log what inputs the agent workflow has
	t.Logf("Agent workflow inputs:")
	for name, schema := range wf.GetInputs() {
		t.Logf("  - %s (type: %s)", name, schema.GetType())
	}

	// Use the exact same code path as the API:
	// preset.NewLoader("").LoadForWorkflow(wf)
	// This validates all presets against the workflow and returns errors
	loader := preset.NewLoader("")
	validPresets, configErrors := loader.LoadForWorkflow(wf)

	t.Logf("Valid presets: %d", len(validPresets))
	for _, p := range validPresets {
		t.Logf("  - %s", p.Name)
	}

	// Add errors to collector (exactly what the API does)
	collector.AddAll(configErrors)

	// Check for errors
	if len(configErrors) > 0 {
		t.Errorf("Found %d config errors from LoadForWorkflow:", len(configErrors))
		for _, err := range configErrors {
			t.Errorf("  [%s] %s: %s", err.Severity, err.Source, err.Message)
		}
	}

	// Also verify the collector has the same errors
	collectedErrors := collector.Errors()
	if len(collectedErrors) != len(configErrors) {
		t.Errorf("Collector has %d errors but LoadForWorkflow returned %d",
			len(collectedErrors), len(configErrors))
	}
}

// TestPresetIncompatibilityNotAddedToHealthCollector verifies that preset
// incompatibilities (presets that don't match a workflow's inputs) are NOT
// added to the health collector. These are expected behaviors, not errors.
func TestPresetIncompatibilityNotAddedToHealthCollector(t *testing.T) {
	// Clear collector
	collector := validation.Global()
	collector.Clear()

	// Use a minimal proto workflow with NO inputs, so all presets are incompatible
	wf := &reliantv1.Workflow{
		Name: "compact",
	}

	// Load presets - this returns incompatibilities but we should NOT add them to collector
	loader := preset.NewLoader("")
	validPresets, incompatibilities := loader.LoadForWorkflow(wf)

	// The compact workflow should have 0 valid presets (no inputs match)
	t.Logf("Compact workflow: %d valid presets, %d incompatibilities",
		len(validPresets), len(incompatibilities))

	// IMPORTANT: We should NOT add incompatibilities to the collector
	// This is the correct behavior - incompatibilities are expected, not errors
	// The API (preset.go) has been fixed to not add these to the collector.

	// Verify the health collector is still empty
	collectedErrors := collector.Errors()
	require.Equal(t, 0, len(collectedErrors),
		"Health collector should be empty - preset incompatibilities are not config errors")
}

// TestBuiltinPresetsValidForAgentWorkflow specifically validates that all
// builtin presets work with the agent workflow (the primary use case).
func TestBuiltinPresetsValidForAgentWorkflow(t *testing.T) {
	// Load the agent workflow
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	require.NoError(t, err, "should read agent workflow")

	wf, err := v2.ParseWorkflowProtoBytes(data)
	require.NoError(t, err, "should parse agent workflow")

	t.Logf("Agent workflow has %d inputs", len(wf.GetInputs()))
	for name, schema := range wf.GetInputs() {
		t.Logf("  - %s (type: %s)", name, schema.GetType())
	}

	// Load all builtin presets
	loader := preset.NewLoader("")
	presets, configErrors := loader.LoadForWorkflow(wf)

	// Log what was loaded
	t.Logf("Loaded %d valid presets for agent workflow", len(presets))
	for _, p := range presets {
		t.Logf("  - %s (tag: %s)", p.Name, p.Tag)
	}

	// Report any validation errors
	if len(configErrors) > 0 {
		t.Errorf("Found %d config errors:", len(configErrors))
		for _, err := range configErrors {
			t.Errorf("  [%s] %s: %s", err.Category, err.Source, err.Message)
			if len(err.Details) > 0 {
				t.Errorf("    Details: %v", err.Details)
			}
		}
	}
}

// TestWorkflowValidation validates all workflows (builtin and sample) parse correctly.
func TestWorkflowValidation(t *testing.T) {
	// Test builtin workflows
	t.Run("builtin", func(t *testing.T) {
		entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
		require.NoError(t, err)

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}

			name := strings.TrimSuffix(entry.Name(), ".yaml")
			t.Run(name, func(t *testing.T) {
				data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
				require.NoError(t, err, "should read workflow file")

				wf, err := wfyaml.ParseWorkflow(data)
				require.NoError(t, err, "should parse workflow")
				require.NotNil(t, wf, "workflow should not be nil")
				require.NotEmpty(t, wf.GetName(), "workflow should have a name")
			})
		}
	})

	// Test sample workflows in workflows/ directory
	t.Run("samples", func(t *testing.T) {
		workflowsDir := findWorkflowsDir()
		if workflowsDir == "" {
			t.Skip("workflows directory not found")
		}

		entries, err := os.ReadDir(workflowsDir)
		if err != nil {
			t.Skip("could not read workflows directory")
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}

			name := strings.TrimSuffix(entry.Name(), ".yaml")
			t.Run(name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(workflowsDir, entry.Name()))
				require.NoError(t, err, "should read workflow file")

				wf, err := wfyaml.ParseWorkflow(data)
				require.NoError(t, err, "should parse workflow")
				require.NotNil(t, wf, "workflow should not be nil")
			})
		}
	})
}

// TestConfigHealthCollectorZeroErrors ensures no config errors are raised
// when validating builtin presets against the agent workflow.
func TestConfigHealthCollectorZeroErrors(t *testing.T) {
	// Clear any existing errors
	collector := validation.Global()
	collector.Clear()

	// Load the agent workflow
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	require.NoError(t, err)

	wf, err := v2.ParseWorkflowProtoBytes(data)
	require.NoError(t, err)

	// Load presets (this adds errors to the collector)
	loader := preset.NewLoader("")
	_, configErrors := loader.LoadForWorkflow(wf)

	// Add errors to collector (simulating what the service does)
	collector.AddAll(configErrors)

	// Check results
	errors := collector.Errors()
	errorCount, warningCount := collector.GetCounts()

	if len(errors) > 0 {
		t.Errorf("Expected 0 config errors, got %d errors and %d warnings:",
			errorCount, warningCount)
		for i, err := range errors {
			t.Errorf("  %d. [%s] %s: %s", i+1, err.Severity, err.Source, err.Message)
		}
	}
}

// TestHealthCollectorDeduplication ensures duplicate errors are not accumulated.
func TestHealthCollectorDeduplication(t *testing.T) {
	collector := validation.Global()
	collector.Clear()

	// Add the same error multiple times
	for i := 0; i < 100; i++ {
		collector.Add(&validation.Error{
			Category: validation.CategoryPreset,
			Source:   "test.yaml",
			Message:  "test error message",
			Severity: validation.SeverityError,
		})
	}

	// Should only have 1 error due to deduplication
	errors := collector.Errors()
	if len(errors) != 1 {
		t.Errorf("Expected 1 error after deduplication, got %d", len(errors))
	}

	// Add a different error
	collector.Add(&validation.Error{
		Category: validation.CategoryPreset,
		Source:   "test.yaml",
		Message:  "different error message",
		Severity: validation.SeverityError,
	})

	errors = collector.Errors()
	if len(errors) != 2 {
		t.Errorf("Expected 2 errors for different messages, got %d", len(errors))
	}

	// Clear and verify
	collector.Clear()
	errors = collector.Errors()
	if len(errors) != 0 {
		t.Errorf("Expected 0 errors after Clear(), got %d", len(errors))
	}
}

// findWorkflowsDir looks for the workflows directory relative to the project root
func findWorkflowsDir() string {
	// Try common paths
	paths := []string{
		"workflows",
		"../workflows",
		"../../workflows",
		"../../../workflows",
	}

	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}
