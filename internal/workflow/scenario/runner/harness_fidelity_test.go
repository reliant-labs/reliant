// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The harness must report what production would: these cases each passed a
// scenario whose production run behaves differently.

const failureVerdictYAML = `
name: verdict-only
entry: [check]
nodes:
  - id: check
    type: compute
    outputs:
      ok: "{{ false }}"
  - id: gave_up
    type: save_message
    outcome: failure
    args:
      role: assistant
      content: "gave up"
  - id: fine
    type: save_message
    args:
      role: assistant
      content: "fine"
edges:
  - from: check
    cases:
      - to: [fine]
        condition: "nodes.check.ok == true"
      - to: [gave_up]
        condition: "true"
`

// A run that ends at a node declaring outcome: failure FAILED in production
// (not archived, on_failure runs, the CLI exits non-zero). The scenario says so.
func TestDeclaredFailureOutcomeIsReportedFailed(t *testing.T) {
	res := runYAMLScenario(t, failureVerdictYAML, &scenario.Scenario{
		Name:   "failed",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeFailed},
	})
	require.Equal(t, "failed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, "gave_up", res.Execution.OutcomeNode)
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
	assert.Empty(t, res.Warnings)
}

// Scenarios written before the verdict was observed assert `completed` on
// such paths. They keep passing, and are told to assert `failed`.
func TestCompletedExpectationOfAFailedRunStillMatchesWithAWarning(t *testing.T) {
	res := runYAMLScenario(t, failureVerdictYAML, &scenario.Scenario{
		Name:   "completed",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeCompleted},
	})
	assert.EqualValues(t, "passed", res.Status, "%v", res.Mismatches)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], `"gave_up"`)
	assert.Contains(t, res.Warnings[0], "outcome: failed")
}

// A failure verdict never satisfies `outcome: error`, and the mismatch says
// which node declared it.
func TestErrorExpectationOfAFailedRunNamesTheVerdictNode(t *testing.T) {
	res := runYAMLScenario(t, failureVerdictYAML, &scenario.Scenario{
		Name:   "error",
		Expect: &scenario.Expectation{Outcome: scenario.OutcomeError},
	})
	assert.EqualValues(t, "failed", res.Status)
	require.Len(t, res.Mismatches, 1)
	assert.Contains(t, res.Mismatches[0], `node "gave_up" declares outcome: failure`)
}

const gateYAML = `
name: gates
entry: [ask]
nodes:
  - id: ask
    type: ask_question
  - id: gate
    type: approval
  - id: done
    type: compute
    outputs:
      answer: "{{ nodes.ask.response }}"
      feedback: "{{ nodes.ask.has_feedback }}"
      status: "{{ nodes.gate.status }}"
edges:
  - from: ask
    default: gate
  - from: gate
    default: done
`

// A mocked response is feedback, as in production; it is not blanked because
// the mock did not also spell has_feedback: true.
func TestAskQuestionResponseWithoutHasFeedbackIsDelivered(t *testing.T) {
	res := runYAMLScenario(t, gateYAML, &scenario.Scenario{
		Name: "answer",
		Events: []scenario.SimulatedEvent{
			{Node: "ask", Output: map[string]interface{}{"response": "Ship it"}},
			{Node: "gate", Output: map[string]interface{}{"status": "approved"}},
		},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	out := res.Execution.NodeOutputs["done"]
	assert.Equal(t, "Ship it", out["answer"])
	assert.Equal(t, true, out["feedback"])
	assert.Empty(t, res.Warnings)
}

// An approval with no scenario status still resolves approved (scenarios rely
// on it), but the run says it tested a path production never takes on its own.
func TestApprovalWithoutAnEventWarns(t *testing.T) {
	res := runYAMLScenario(t, gateYAML, &scenario.Scenario{
		Name:   "silent",
		Events: []scenario.SimulatedEvent{{Node: "ask", Output: map[string]interface{}{"response": "ok"}}},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, "approved", res.Execution.NodeOutputs["done"]["status"])
	require.Len(t, res.Warnings, 1, "%v", res.Warnings)
	assert.Contains(t, res.Warnings[0], `approval "gate" had no status`)
}

// A mock shape the runtime cannot produce is a validation error, not a pass.
func TestImpossibleGateMocksAreRejected(t *testing.T) {
	for name, ev := range map[string]scenario.SimulatedEvent{
		"unknown approval status": {Node: "gate", Output: map[string]interface{}{"status": "rejected"}},
		"response without feedback": {Node: "ask",
			Output: map[string]interface{}{"response": "x", "has_feedback": false}},
		"raw answer payload": {Node: "ask",
			Output: map[string]interface{}{"response": `{"answers":[{"selected":["Approve"]}]}`}},
	} {
		t.Run(name, func(t *testing.T) {
			res := runYAMLScenario(t, gateYAML, &scenario.Scenario{Name: "bad", Events: []scenario.SimulatedEvent{ev}})
			assert.EqualValues(t, "error", res.Status)
			assert.Contains(t, strings.Join(res.Mismatches, "\n"), "event[0]")
		})
	}
}

// A budget hit blames a loop only when a loop was re-entering its body.
func TestBudgetExhaustedMessage(t *testing.T) {
	spin := budgetExhaustedMessage(20*time.Second, map[string]int{"poll": 412, "outer": 1})
	assert.True(t, strings.HasPrefix(spin, nonTerminatingPrefix), spin)
	assert.Contains(t, spin, `loop "poll" was still iterating after 412 iteration(s)`)
	assert.Contains(t, spin, "cut off after 20s")

	for _, loops := range []map[string]int{nil, {"body": 1}} {
		busy := budgetExhaustedMessage(20*time.Second, loops)
		assert.True(t, strings.HasPrefix(busy, nonTerminatingPrefix), busy)
		assert.Contains(t, busy, "not a non-terminating loop")
		assert.NotContains(t, busy, "while condition")
	}
}
