// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	// Import activities to trigger init() which registers activity schemas.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// TestBuiltinWorkflowScenarios runs all scenario tests for builtin workflows.
//
// For each builtin workflow (e.g., agent.yaml), it looks for a scenario file
// at testdata/<workflow-name>_scenarios.yaml and runs each scenario through
// the simulator engine.
//
// Scenario files can contain multiple YAML documents separated by ---.
func TestBuiltinWorkflowScenarios(t *testing.T) {
	t.Parallel()
	// Get all builtin workflows
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Failed to read builtin workflows directory")

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}

		workflowFile := entry.Name()
		workflowName := strings.TrimSuffix(workflowFile, ".yaml")
		workflowName = strings.TrimSuffix(workflowName, ".yml")

		// Load the workflow
		workflowData, err := builtin.BuiltinWorkflowsFS.ReadFile(workflowFile)
		require.NoError(t, err, "Failed to read workflow %s", workflowFile)

		wf, err := v2.ParseWorkflowProtoBytesWithLoader(workflowData, builtinLoader)
		require.NoError(t, err, "Failed to parse workflow %s", workflowFile)

		// Run full structural validation (same as runtime uses)
		result := validation.StaticAnalysis(wf, builtinLoader)
		require.NoError(t, result.AsError(), "Workflow validation failed for %s", workflowFile)

		// Find scenarios for this workflow
		scenarios, err := loadScenariosForWorkflow(workflowName)
		if err != nil {
			// No scenarios file is OK - just skip this workflow
			continue
		}

		if len(scenarios) == 0 {
			continue
		}

		t.Run(workflowName, func(t *testing.T) {
			engine := simulator.NewEngine(wf)

			for _, scenario := range scenarios {
				t.Run(scenario.Name, func(t *testing.T) {
					result := engine.RunScenario(scenario)

					// Report detailed results on failure
					if result.Status != simulator.StatusPassed {
						t.Logf("Scenario: %s", scenario.Name)
						t.Logf("Description: %s", scenario.Description)
						t.Logf("Outcome: %s", result.Execution.Outcome)
						t.Logf("Nodes reached: %v", result.Execution.NodesReached)
						if result.Execution.Error != nil {
							t.Logf("Error: %s (node: %s)", result.Execution.Error.Message, result.Execution.Error.Node)
						}
						for _, mismatch := range result.Mismatches {
							t.Errorf("Mismatch: %s", mismatch)
						}
					}

					assert.Equal(t, simulator.StatusPassed, result.Status,
						"Scenario %q failed with %d mismatches", scenario.Name, len(result.Mismatches))
				})
			}
		})
	}
}

// loadScenariosForWorkflow loads all scenarios for a workflow from BOTH sources:
//  1. testdata/<workflow>_scenarios.yaml (multi-document or wrapper format)
//  2. scenarios/<workflow>/*.yaml (one or more scenarios per file)
//
// This mirrors the CLI's scenario discovery (findScenariosForWorkflow in
// cmd/reliant/commands/workflow.go) so `go test` exercises every scenario.
func loadScenariosForWorkflow(workflowName string) ([]*simulator.Scenario, error) {
	var allScenarios []*simulator.Scenario

	// Co-located testdata file
	scenarioFile := "testdata/" + workflowName + "_scenarios.yaml"
	if data, err := builtin.BuiltinScenariosFS.ReadFile(scenarioFile); err == nil {
		scenarios, err := parseMultiDocYAML(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", scenarioFile, err)
		}
		allScenarios = append(allScenarios, scenarios...)
	}

	// scenarios/<workflow>/ directory
	scenarioDir := "scenarios/" + workflowName
	if entries, err := builtin.BuiltinScenarioDirsFS.ReadDir(scenarioDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
				continue
			}
			data, err := builtin.BuiltinScenarioDirsFS.ReadFile(scenarioDir + "/" + entry.Name())
			if err != nil {
				return nil, err
			}
			scenarios, err := parseMultiDocYAML(data)
			if err != nil {
				return nil, fmt.Errorf("parse %s/%s: %w", scenarioDir, entry.Name(), err)
			}
			allScenarios = append(allScenarios, scenarios...)
		}
	}

	if len(allScenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found for workflow %s", workflowName)
	}

	return allScenarios, nil
}

// scenarioFile is a wrapper format for scenario files with an array of scenarios
type scenarioFile struct {
	APIVersion string                `yaml:"apiVersion"`
	Scenarios  []*simulator.Scenario `yaml:"scenarios"`
}

// parseMultiDocYAML parses a YAML file with either:
// 1. Multiple documents separated by --- (each is a scenario)
// 2. A wrapper format with apiVersion and scenarios array
func parseMultiDocYAML(data []byte) ([]*simulator.Scenario, error) {
	var scenarios []*simulator.Scenario

	// First try to parse as wrapper format
	var wrapper scenarioFile
	if err := yaml.Unmarshal(data, &wrapper); err == nil && len(wrapper.Scenarios) > 0 {
		return wrapper.Scenarios, nil
	}

	// Fall back to multi-document format
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var scenario simulator.Scenario
		err := decoder.Decode(&scenario)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Skip empty documents
		if scenario.Name == "" {
			continue
		}
		scenarios = append(scenarios, &scenario)
	}

	return scenarios, nil
}

// TestScenarioFilesAreValid validates that all scenario files parse correctly
// without actually running them. This catches YAML syntax errors early.
func TestScenarioFilesAreValid(t *testing.T) {
	t.Parallel()
	err := fs.WalkDir(builtin.BuiltinScenariosFS, "testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// If testdata directory doesn't exist, that's OK
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_scenarios.yaml") {
			return nil
		}

		t.Run(path, func(t *testing.T) {
			data, err := builtin.BuiltinScenariosFS.ReadFile(path)
			require.NoError(t, err, "Failed to read scenario file")

			scenarios, err := parseMultiDocYAML(data)
			require.NoError(t, err, "Failed to parse scenario YAML")

			require.NotEmpty(t, scenarios, "Scenario file must contain at least one scenario")

			for _, scenario := range scenarios {
				assert.NotEmpty(t, scenario.Name, "Each scenario must have a name")
				// Scenario must have either events OR state (for state injection)
				hasEvents := len(scenario.Events) > 0
				hasState := len(scenario.State) > 0
				assert.True(t, hasEvents || hasState, "Scenario %q must have at least one event or use state injection", scenario.Name)
			}
		})
		return nil
	})

	if err != nil {
		t.Logf("Note: testdata directory walk returned error (may be empty): %v", err)
	}
}

// TestScenarioDirsMapToWorkflows verifies every scenarios/<name>/ directory
// corresponds to an existing builtin workflow. Orphaned scenario directories
// are dead tests that can never run — they must be updated or removed when a
// workflow is renamed or deleted.
func TestScenarioDirsMapToWorkflows(t *testing.T) {
	t.Parallel()
	entries, err := builtin.BuiltinScenarioDirsFS.ReadDir("scenarios")
	if err != nil {
		t.Skip("no scenarios directory embedded")
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workflowFile := entry.Name() + ".yaml"
		if _, err := builtin.BuiltinWorkflowsFS.ReadFile(workflowFile); err != nil {
			t.Errorf("scenarios/%s/ has no matching builtin workflow %s — update or remove these scenarios", entry.Name(), workflowFile)
		}
	}
}

// TestAllWorkflowsHaveScenarios checks that each workflow has a corresponding
// scenario file. This is informational - not all workflows require scenarios.
func TestAllWorkflowsHaveScenarios(t *testing.T) {
	t.Parallel()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	var missing []string
	var covered []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		workflowName := strings.TrimSuffix(entry.Name(), ".yaml")
		scenarioFile := "testdata/" + workflowName + "_scenarios.yaml"

		if _, err := builtin.BuiltinScenariosFS.ReadFile(scenarioFile); err != nil {
			missing = append(missing, workflowName)
		} else {
			covered = append(covered, workflowName)
		}
	}

	t.Logf("Workflows with scenarios (%d): %v", len(covered), covered)
	t.Logf("Workflows without scenarios (%d): %v", len(missing), missing)
}

// builtinLoader resolves builtin:// workflow references for cross-workflow validation.
func builtinLoader(name string) (*reliantv1.Workflow, error) {
	// Strip builtin:// prefix if present
	name = strings.TrimPrefix(name, "builtin://")
	filename := name + ".yaml"
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("builtin workflow not found: %s", name)
	}
	return wfyaml.ParseWorkflow(data)
}
