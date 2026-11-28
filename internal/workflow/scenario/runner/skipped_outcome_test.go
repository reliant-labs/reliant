// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const skippedVerdictYAML = `
name: verdict-skipped
entry: [check]
nodes:
  - id: check
    type: compute
    outputs:
      healthy: "{{ false }}"
  - id: unhealthy
    type: save_message
    outcome: skipped
    args:
      role: assistant
      content: "environment unhealthy; asks again next tick"
  - id: work
    type: save_message
    args:
      role: assistant
      content: "did the work"
edges:
  - from: check
    cases:
      - to: [work]
        condition: "nodes.check.healthy == true"
      - to: [unhealthy]
        condition: "true"
`

// A run that ends at a node declaring outcome: skipped completed and chose
// not to do its work; production records run_outcome skipped. The scenario
// reports "skipped" and names the node, and `outcome: skipped` matches it.
func TestDeclaredSkippedOutcomeIsReportedSkipped(t *testing.T) {
	res := runYAMLScenario(t, skippedVerdictYAML, &scenario.Scenario{
		Name:   "skipped",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeSkipped},
	})
	require.Equal(t, "skipped", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, "unhealthy", res.Execution.OutcomeNode)
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
	assert.Empty(t, res.Warnings)
}

// The legacy `outcome: completed` still matches a skipped run (it did
// complete its graph), with a warning to assert `skipped`; `outcome: failed`
// never matches one, and the mismatch names the declaring node.
func TestCompletedAndFailedExpectationsOfASkippedRun(t *testing.T) {
	res := runYAMLScenario(t, skippedVerdictYAML, &scenario.Scenario{
		Name:   "completed",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeCompleted},
	})
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], `"unhealthy"`)
	assert.Contains(t, res.Warnings[0], "outcome: skipped")

	res = runYAMLScenario(t, skippedVerdictYAML, &scenario.Scenario{
		Name:   "failed",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeFailed},
	})
	assert.EqualValues(t, "failed", res.Status)
	require.Len(t, res.Mismatches, 1)
	assert.Contains(t, res.Mismatches[0], `node "unhealthy" declares outcome: skipped`)
}
