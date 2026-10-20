// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuiltinWorkflowScenarios runs every builtin scenario
// (<workflow>/scenarios/*.yaml) on the scenario runner, i.e. through the REAL
// DynamicWorkflow with only the activity layer mocked. There is no allowlist of
// known failures: a scenario that does not pass here is a scenario the runtime
// does not satisfy.
func TestBuiltinWorkflowScenarios(t *testing.T) {
	t.Parallel()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "Failed to read builtin workflows directory")
	layout := builtinLayout(t)

	ran := 0
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}

		workflowFile := entry.Name()
		workflowName := strings.TrimSuffix(strings.TrimSuffix(workflowFile, ".yaml"), ".yml")

		wf, err := parseBuiltin(workflowFile)
		require.NoError(t, err, "Failed to parse workflow %s", workflowFile)

		// Full structural validation (same as the runtime uses).
		result := analyzeBuiltin(workflowFile)
		require.NoError(t, result.AsError(), "Workflow validation failed for %s", workflowFile)

		scenarios, err := loadScenariosForWorkflow(t, layout, workflowName)
		if err != nil {
			// "no scenarios" is fine; a scenario file that does not parse is not. Skipping it
			// silently dropped every scenario of the workflow, including the ones that parse.
			require.Contains(t, err.Error(), "no scenarios found", "scenarios of %s", workflowName)
			continue
		}
		if len(scenarios) == 0 {
			continue
		}
		ran += len(scenarios)

		t.Run(workflowName, func(t *testing.T) {
			t.Parallel()
			r := runner.NewRunner(wf)
			for _, sc := range scenarios {
				t.Run(sc.Name, func(t *testing.T) {
					res := r.Run(sc)
					if res.Status == wfscenario.StatusPassed {
						return
					}
					t.Logf("Description: %s", sc.Description)
					t.Logf("Outcome: %s", res.Execution.Outcome)
					t.Logf("Nodes reached: %v", res.Execution.NodesReached)
					if res.Execution.Error != nil {
						t.Logf("Error: %s", res.Execution.Error.Message)
					}
					for _, m := range res.Mismatches {
						t.Errorf("Mismatch: %s", m)
					}
					t.Errorf("scenario %q: status %s", sc.Name, res.Status)
				})
			}
		})
	}
	require.NotZero(t, ran, "no builtin scenarios were found")
}

// builtinLayout lays the builtin workflows and their scenario directories out
// the way `reliant workflow scenario run --include-builtins` reads them.
func builtinLayout(t *testing.T) *workflowref.Layout {
	t.Helper()
	layout, err := workflowref.BuiltinLayout()
	require.NoError(t, err)
	return layout
}

// loadScenariosForWorkflow parses a workflow's <workflow>/scenarios/*.yaml, one
// scenario per file, exactly as `reliant workflow scenario run` reads a
// project's (the shared locator and wfscenario.ParseScenarioFile).
func loadScenariosForWorkflow(t *testing.T, layout *workflowref.Layout, workflowName string) ([]*wfscenario.Scenario, error) {
	t.Helper()
	var allScenarios []*wfscenario.Scenario
	for _, f := range layout.ScenariosFor(workflowref.Slug(workflowName)) {
		sc, err := wfscenario.ParseScenarioFile(f.Content, f.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		allScenarios = append(allScenarios, sc)
	}
	if len(allScenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found for workflow %s", workflowName)
	}
	return allScenarios, nil
}

// TestScenarioFilesAreValid checks every builtin scenario file parses, drives
// the run and asserts something, without running it.
func TestScenarioFilesAreValid(t *testing.T) {
	t.Parallel()
	layout := builtinLayout(t)
	require.NotEmpty(t, layout.Scenarios)
	for _, f := range layout.Scenarios {
		t.Run(f.Path, func(t *testing.T) {
			scenario, err := wfscenario.ParseScenarioFile(f.Content, f.Name)
			require.NoError(t, err)
			// A scenario drives the run with events, injected state or inputs
			// alone (a guard that refuses before any node runs), and asserts
			// what happened.
			drives := len(scenario.Events) > 0 || len(scenario.State) > 0 || len(scenario.Inputs) > 0
			assert.True(t, drives, "Scenario %q must supply events, state or inputs", scenario.Name)
			assert.NotNil(t, scenario.Expect, "Scenario %q must assert something under expect:", scenario.Name)
		})
	}
}

// TestScenarioDirsMapToWorkflows verifies every <name>/scenarios/ directory
// belongs to an existing builtin workflow, by the locator's own rule
// (workflowref: the directory is the workflow's name: as a slug). Orphaned
// scenario directories are dead tests that can never run — they must be
// updated or removed when a workflow is renamed or deleted.
func TestScenarioDirsMapToWorkflows(t *testing.T) {
	t.Parallel()
	layout := builtinLayout(t)
	require.NotEmpty(t, layout.Scenarios, "no scenario directories embedded")
	for _, problem := range layout.ScenarioProblems() {
		t.Error(problem)
	}
}

// TestAllWorkflowsHaveScenarios checks that each workflow has a corresponding
// scenario file. This is informational - not all workflows require scenarios.
func TestAllWorkflowsHaveScenarios(t *testing.T) {
	t.Parallel()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)
	layout := builtinLayout(t)

	var missing []string
	var covered []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		workflowName := strings.TrimSuffix(entry.Name(), ".yaml")

		if len(layout.ScenariosFor(workflowref.Slug(workflowName))) == 0 {
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
