// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"

	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// failingRoutingResult mirrors what the runner produces for a routing test
// that took the wrong branch: `handle_question` was skipped rather than run,
// `handle_command` ran instead, and the scenario asserted the opposite.
func failingRoutingResult() *wfscenario.ScenarioResult {
	return &wfscenario.ScenarioResult{
		Status:   wfscenario.StatusFailed,
		Scenario: "routes_question_to_handler",
		Execution: wfscenario.ExecutionDetails{
			NodesReached:   []string{"classify", "handle_question", "handle_command", "respond"},
			NodesCompleted: []string{"classify", "handle_command", "respond"},
			NodesSkipped:   []string{"handle_question"},
			NodeStates: map[string]wfscenario.NodeExecutionState{
				"classify":        wfscenario.StateCompleted,
				"handle_question": wfscenario.StateSkipped,
				"handle_command":  wfscenario.StateCompleted,
				"respond":         wfscenario.StateCompleted,
			},
			Outcome:    "completed",
			DurationMs: 4,
			NodeOutputs: map[string]map[string]interface{}{
				"classify":       {"category": "command", "confidence": 0.42},
				"handle_command": {"result": strings.Repeat("x", 900)},
				"respond":        {"response_text": "done"},
			},
			WorkflowOutputs: map[string]interface{}{
				"answer": "ran the command",
				"route":  "command",
			},
		},
		Expected: &wfscenario.Expectation{
			Outcome:    wfscenario.OutcomeCompleted,
			Completed:  []string{"handle_question"},
			NotReached: []string{"handle_command"},
		},
		Mismatches: []string{
			`expected node "handle_question" to be completed but it wasn't (completed: [classify handle_command respond])`,
			`expected node "handle_command" NOT to be reached but it was`,
		},
	}
}

// TestFormatScenarioResult_FailureNamesExpectationAndNode pins the property the
// formatter exists for: a failing run must name the assertion that failed AND
// let the reader see how the diverging node actually behaved. The old formatter
// printed only NodesReached, so a skipped node and a completed one were
// indistinguishable and this test failed on the "skipped" assertions.
func TestFormatScenarioResult_FailureNamesExpectationAndNode(t *testing.T) {
	out := formatScenarioResultInternal(failingRoutingResult())

	for _, want := range []string{
		"FAILED",
		"handle_question",   // the node that diverged
		"to be completed",   // the specific failed expectation
		"handle_command",    // the node that ran when it shouldn't have
		"NOT to be reached", // the second failed expectation
		"skipped",           // handle_question's actual state
		"answer",            // workflow outputs are surfaced
		"category",          // node outputs are surfaced
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted failure output is missing %q\n--- output ---\n%s", want, out)
		}
	}

	// The state of the diverging node must be attached to the failure, not left
	// for the reader to infer from a flat list of reached nodes.
	if !strings.Contains(out, `handle_question`) || !strings.Contains(out, "skipped") {
		t.Fatalf("expected the failure to report handle_question as skipped:\n%s", out)
	}

	// Large node outputs must be truncated - this text goes into a context window.
	if strings.Contains(out, strings.Repeat("x", 400)) {
		t.Errorf("large node output was not truncated:\n%s", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected a truncation marker in output:\n%s", out)
	}
}

// TestFormatScenarioResult_PassStaysSmall guards the other failure mode: a
// verbose formatter pushes the workflow YAML out of the model's context.
func TestFormatScenarioResult_PassStaysSmall(t *testing.T) {
	result := failingRoutingResult()
	result.Status = wfscenario.StatusPassed
	result.Mismatches = nil

	out := formatScenarioResultInternal(result)

	if !strings.Contains(out, "PASSED") {
		t.Fatalf("expected PASSED in output:\n%s", out)
	}
	if strings.Contains(out, "confidence") {
		t.Errorf("per-node outputs should not be dumped on success:\n%s", out)
	}
	if len(out) > 600 {
		t.Errorf("passing output is %d bytes, expected it to stay compact:\n%s", len(out), out)
	}
}

// TestFormatScenarioResult_ErrorShowsFailingNode covers the error outcome path.
func TestFormatScenarioResult_ErrorShowsFailingNode(t *testing.T) {
	result := &wfscenario.ScenarioResult{
		Status:   wfscenario.StatusError,
		Scenario: "handles_llm_error",
		Execution: wfscenario.ExecutionDetails{
			NodesReached:   []string{"call_llm"},
			NodesCompleted: []string{},
			Outcome:        "error",
			DurationMs:     1,
			Error: &wfscenario.ErrorDetails{
				Node:       "call_llm",
				Step:       "evaluate",
				Message:    "rate limit exceeded",
				Expression: "${{ steps.call_llm.output }}",
			},
		},
		Expected: &wfscenario.Expectation{Outcome: wfscenario.OutcomeCompleted},
		Mismatches: []string{
			`expected outcome "completed" but got "error"`,
		},
	}

	out := formatScenarioResultInternal(result)
	for _, want := range []string{"call_llm", "rate limit exceeded", "evaluate", `expected outcome`} {
		if !strings.Contains(out, want) {
			t.Errorf("error output missing %q\n--- output ---\n%s", want, out)
		}
	}
}
