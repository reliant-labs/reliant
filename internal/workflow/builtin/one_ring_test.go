// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	// Import activities to trigger init() which registers activity schemas.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// TestOneRingWorkflowScenarios runs all scenario tests for the one-ring workflow.
//
// The one-ring workflow structure:
//   - Planning phase (sub-workflow): plan
//   - TDD: write_tests
//   - Implementation loop: implement → lint/test/build → evaluate
//   - Loop exits on success or max_retries
func TestOneRingWorkflowScenarios(t *testing.T) {
	t.Parallel()
	workflowData, err := builtin.BuiltinWorkflowsFS.ReadFile("one-ring.yaml")
	require.NoError(t, err, "Failed to read one-ring.yaml")

	wf, err := v2.ParseWorkflowProtoBytes(workflowData)
	require.NoError(t, err, "Failed to parse one-ring workflow")

	scenarios, err := loadOneRingScenarios()
	require.NoError(t, err, "Failed to load scenarios")
	require.NotEmpty(t, scenarios, "No scenarios loaded")

	engine := simulator.NewEngine(wf)

	var passed, failed int

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			result := engine.RunScenario(scenario)

			t.Logf("Scenario: %s", scenario.Name)
			t.Logf("Description: %s", scenario.Description)
			t.Logf("Status: %s", result.Status)
			t.Logf("Outcome: %s", result.Execution.Outcome)
			t.Logf("Nodes reached: %v", result.Execution.NodesReached)

			if result.Execution.Error != nil {
				t.Logf("Error: %s (node: %s)", result.Execution.Error.Message, result.Execution.Error.Node)
			}

			for _, mismatch := range result.Mismatches {
				t.Logf("Mismatch: %s", mismatch)
			}

			if passingScenarios[scenario.Name] {
				if result.Status == simulator.StatusPassed {
					passed++
					t.Logf("✓ Scenario passed as expected")
				} else {
					failed++
					t.Errorf("✗ Expected scenario to pass but got status: %s", result.Status)
					for _, m := range result.Mismatches {
						t.Errorf("  - %s", m)
					}
				}
			} else if bugScenarios[scenario.Name] {
				if result.Status != simulator.StatusPassed {
					t.Logf("✗ Scenario fails as expected (documents known limitation)")
				} else {
					passed++
					t.Logf("✓ Scenario passed! (Limitation may be fixed)")
				}
			}
		})
	}

	t.Logf("\n=== Summary ===")
	t.Logf("Passed: %d", passed)
	t.Logf("Failed: %d", failed)
	t.Logf("Total: %d", len(scenarios))
}

// passingScenarios lists all scenarios expected to pass.
var passingScenarios = map[string]bool{
	"default_steps":              true,
	"plan_only":                  true,
	"full_pipeline_all_pass":     true,
	"implement_only":             true,
	"evaluate_fails_then_passes": true,
	"build_fails_then_passes":    true,
	"lint_fails_then_passes":     true,
	"test_fails_then_passes":     true,
	"multiple_checks_fail":       true,
	"max_retries_exhausted":      true,
	"start_app_with_ux_review":   true,
}

// bugScenarios documents known limitations (none currently).
var bugScenarios = map[string]bool{}

// TestOneRingWorkflowScenarios_Passing runs only the scenarios expected to pass.
// Use this for CI.
func TestOneRingWorkflowScenarios_Passing(t *testing.T) {
	t.Parallel()
	workflowData, err := builtin.BuiltinWorkflowsFS.ReadFile("one-ring.yaml")
	require.NoError(t, err)

	wf, err := v2.ParseWorkflowProtoBytes(workflowData)
	require.NoError(t, err)

	scenarios, err := loadOneRingScenarios()
	require.NoError(t, err)

	engine := simulator.NewEngine(wf)

	for name := range passingScenarios {
		var scenario *simulator.Scenario
		for _, s := range scenarios {
			if s.Name == name {
				scenario = s
				break
			}
		}
		if scenario == nil {
			t.Errorf("Scenario %q not found", name)
			continue
		}

		t.Run(name, func(t *testing.T) {
			result := engine.RunScenario(scenario)

			if result.Status != simulator.StatusPassed {
				t.Logf("Outcome: %s", result.Execution.Outcome)
				t.Logf("Nodes reached: %v", result.Execution.NodesReached)
				for _, m := range result.Mismatches {
					t.Errorf("Mismatch: %s", m)
				}
			}

			assert.Equal(t, simulator.StatusPassed, result.Status)
		})
	}
}

// TestOneRingWorkflowScenarios_BugDemonstration documents known limitations.
func TestOneRingWorkflowScenarios_BugDemonstration(t *testing.T) {
	t.Parallel()
	workflowData, err := builtin.BuiltinWorkflowsFS.ReadFile("one-ring.yaml")
	require.NoError(t, err)

	wf, err := v2.ParseWorkflowProtoBytes(workflowData)
	require.NoError(t, err)

	scenarios, err := loadOneRingScenarios()
	require.NoError(t, err)

	engine := simulator.NewEngine(wf)

	for name := range bugScenarios {
		var scenario *simulator.Scenario
		for _, s := range scenarios {
			if s.Name == name {
				scenario = s
				break
			}
		}
		if scenario == nil {
			t.Errorf("Scenario %q not found", name)
			continue
		}

		t.Run(name, func(t *testing.T) {
			result := engine.RunScenario(scenario)

			t.Logf("Status: %s", result.Status)
			t.Logf("Outcome: %s", result.Execution.Outcome)
			t.Logf("Nodes reached: %v", result.Execution.NodesReached)

			if result.Status != simulator.StatusPassed {
				t.Logf("LIMITATION CONFIRMED: Scenario fails as expected")
				for _, m := range result.Mismatches {
					t.Logf("  - %s", m)
				}
			} else {
				t.Logf("LIMITATION MAY BE FIXED: Scenario now passes!")
			}
		})
	}
}

func loadOneRingScenarios() ([]*simulator.Scenario, error) {
	data, err := builtin.BuiltinScenariosFS.ReadFile("testdata/one-ring_scenarios.yaml")
	if err != nil {
		return nil, err
	}

	var scenarios []*simulator.Scenario
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
		if scenario.Name == "" {
			continue
		}
		scenarios = append(scenarios, &scenario)
	}

	return scenarios, nil
}
