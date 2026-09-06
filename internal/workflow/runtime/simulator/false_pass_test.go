// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// A workflow that delegates to a referenced sub-workflow. Scenarios can either
// black-box `do_work` (its output comes from the scenario, the sub-workflow
// never runs) or mock its internals with qualified ids.
const falsePassRefWorkflow = `
name: false-pass-ref
apiVersion: "1.0"
entry: [do_work]
nodes:
  - id: do_work
    type: workflow
    ref: builtin://inner-worker
  - id: finish
    type: save_message
    args:
      role: "assistant"
      content: "done"
edges:
  - from: do_work
    default: finish
`

// The sub-workflow `do_work` refers to. Loaded by the test loader below.
const falsePassInnerWorkflow = `
name: inner-worker
apiVersion: "1.0"
entry: [inner_llm]
nodes:
  - id: inner_llm
    type: call_llm
    model:
      tags: [fast]
  - id: inner_save
    type: save_message
    args:
      role: "assistant"
      content: "inner"
edges:
  - from: inner_llm
    default: inner_save
`

// A workflow whose entry is a node router with three candidates.
const falsePassRouterWorkflow = `
name: false-pass-router
apiVersion: "1.0"
entry: [classify]
nodes:
  - id: classify
    type: router
    model:
      tags: [fast]
    system_prompt: "Pick a branch."
    nodes:
      - id: handle_simple
        description: "Simple request."
      - id: handle_complex
        description: "Complex request."
    fallback: handle_simple
  - id: handle_simple
    type: call_llm
    model:
      tags: [fast]
  - id: handle_complex
    type: call_llm
    model:
      tags: [fast]
`

// falsePassLoader resolves the one ref used by these fixtures.
func falsePassLoader(t *testing.T) func(string) (*reliantv1.Workflow, error) {
	t.Helper()
	inner, err := ParseWorkflowYAML([]byte(falsePassInnerWorkflow))
	require.NoError(t, err)
	return func(ref string) (*reliantv1.Workflow, error) {
		return inner, nil
	}
}

func runFalsePassScenario(t *testing.T, workflowYAML string, scenario *Scenario) *ScenarioResult {
	t.Helper()
	wf, err := ParseWorkflowYAML([]byte(workflowYAML))
	require.NoError(t, err)
	engine := NewEngineWithLoader(wf, falsePassLoader(t))
	return engine.RunScenario(scenario)
}

// requireWarningContaining asserts exactly one warning matches, and returns it.
func requireWarningContaining(t *testing.T, warnings []string, substr string) string {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return w
		}
	}
	t.Fatalf("no warning containing %q; got %#v", substr, warnings)
	return ""
}

func requireNoWarningContaining(t *testing.T, warnings []string, substr string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			t.Fatalf("unexpected warning containing %q: %s", substr, w)
		}
	}
}

func TestAnalyzeFalsePasses_BlackBoxSubWorkflowWarns(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRefWorkflow, &Scenario{
		Name: "black_box",
		Events: []SimulatedEvent{
			{Node: "do_work", Output: map[string]interface{}{"result": "pretend it worked"}},
			{Node: "finish", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted, Reached: []string{"do_work", "finish"}},
	})

	// The point of the warning: the scenario PASSES while the sub-workflow
	// never ran. If it stopped passing, the warning would be redundant.
	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.NotContains(t, result.Execution.NodesReached, "do_work.inner_llm",
		"sub-workflow should not have executed in black-box mode")

	w := requireWarningContaining(t, result.Warnings, "black-box sub-workflow")
	require.Contains(t, w, `"do_work"`, "warning must name the node")
	require.Contains(t, w, "builtin://inner-worker", "warning must name the ref")
	require.Contains(t, w, "did NOT execute")
	require.Contains(t, w, "all 2 of its nodes", "warning should quantify what was skipped")
	require.Contains(t, w, "do_work.inner_llm", "warning must show the qualified-id form to use")
}

func TestAnalyzeFalsePasses_QualifiedSubWorkflowEventsDoNotWarn(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRefWorkflow, &Scenario{
		Name: "internal",
		Events: []SimulatedEvent{
			{Node: "do_work.inner_llm", Type: "llm_response", Text: "inner done"},
			{Node: "do_work.inner_save", Output: map[string]interface{}{}},
			{Node: "finish", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	// Non-vacuous: prove the sub-workflow really executed this time.
	require.Contains(t, result.Execution.NodesReached, "do_work.inner_llm")
	requireNoWarningContaining(t, result.Warnings, "black-box sub-workflow")
}

func TestAnalyzeFalsePasses_InlineSubWorkflowDoesNotWarn(t *testing.T) {
	const inlineWorkflow = `
name: false-pass-inline
apiVersion: "1.0"
entry: [do_work]
nodes:
  - id: do_work
    type: workflow
    inline:
      name: inline-worker
      entry: [inner_llm]
      nodes:
        - id: inner_llm
          type: call_llm
          model:
            tags: [fast]
`
	result := runFalsePassScenario(t, inlineWorkflow, &Scenario{
		Name:   "inline",
		Events: []SimulatedEvent{{Node: "do_work.inner_llm", Type: "llm_response", Text: "hi"}},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	requireNoWarningContaining(t, result.Warnings, "black-box sub-workflow")
}

func TestAnalyzeFalsePasses_DefaultedRouterWarns(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRouterWorkflow, &Scenario{
		Name: "router_default",
		Events: []SimulatedEvent{
			{Node: "handle_simple", Type: "llm_response", Text: "simple path"},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted, Reached: []string{"handle_simple"}},
	})

	// Passes green having asserted a route the router was never asked to pick.
	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)

	w := requireWarningContaining(t, result.Warnings, "unmocked node router")
	require.Contains(t, w, `"classify"`, "warning must name the router node")
	require.Contains(t, w, `"handle_simple"`, "warning must name the default it took")
	require.Contains(t, w, "handle_complex", "warning must list the other candidates")
	require.Contains(t, w, "routing was NOT tested")
}

func TestAnalyzeFalsePasses_MockedRouterDoesNotWarn(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRouterWorkflow, &Scenario{
		Name: "router_mocked",
		Events: []SimulatedEvent{
			{Node: "classify", Output: map[string]interface{}{
				"selected_node": "handle_complex",
				"reasoning":     "the request needs multiple steps",
			}},
			{Node: "handle_complex", Type: "llm_response", Text: "complex path"},
		},
		Expect: &Expectation{
			Outcome:    OutcomeCompleted,
			Reached:    []string{"handle_complex"},
			NotReached: []string{"handle_simple"},
		},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	// Non-vacuous: the mock actually steered routing away from candidates[0].
	require.Contains(t, result.Execution.NodesReached, "handle_complex")
	requireNoWarningContaining(t, result.Warnings, "unmocked node router")
}

// Deliberately mocking the FIRST candidate is indistinguishable from the
// default by output alone — both end with selected_node == candidates[0]. Only
// the pre-run snapshot separates "the author chose this" from "the simulator
// filled it in", so this is the case that pins SnapshotRouterMocks.
func TestAnalyzeFalsePasses_RouterMockedToFirstCandidateDoesNotWarn(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRouterWorkflow, &Scenario{
		Name: "router_mocked_first",
		Events: []SimulatedEvent{
			{Node: "classify", Output: map[string]interface{}{"selected_node": "handle_simple"}},
			{Node: "handle_simple", Type: "llm_response", Text: "simple path"},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted, Reached: []string{"handle_simple"}},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	require.Equal(t, "handle_simple", result.Execution.NodeOutputs["classify"]["selected_node"],
		"same selection as the default — only the snapshot can tell them apart")
	requireNoWarningContaining(t, result.Warnings, "unmocked node router")
}

// A scenario that mocks `reasoning` but forgets `selected_node` suppresses the
// simulator's default marker, so the detection must fall back to comparing the
// selection against candidates[0].
func TestAnalyzeFalsePasses_RouterWithReasoningOnlyStillWarns(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRouterWorkflow, &Scenario{
		Name: "router_reasoning_only",
		Events: []SimulatedEvent{
			{Node: "classify", Output: map[string]interface{}{"reasoning": "author wrote prose, not a decision"}},
			{Node: "handle_simple", Type: "llm_response", Text: "simple path"},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	requireWarningContaining(t, result.Warnings, "unmocked node router")
}

// Warnings must never change pass/fail — the corpus depends on it.
func TestAnalyzeFalsePasses_WarningsDoNotAffectStatus(t *testing.T) {
	result := runFalsePassScenario(t, falsePassRefWorkflow, &Scenario{
		Name: "warned_but_passing",
		Events: []SimulatedEvent{
			{Node: "do_work", Output: map[string]interface{}{"result": "x"}},
			{Node: "finish", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{Outcome: OutcomeCompleted},
	})

	require.NotEmpty(t, result.Warnings)
	require.Equal(t, StatusPassed, result.Status)
	require.Empty(t, result.Mismatches)
}
