// Copyright (c) 2025 Reliant Labs
package validation

import (
	"sort"
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortedNodeIDs returns node IDs from a set in deterministic order.
func sortedNodeIDs(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// orderingIssues returns all node_ordering findings from a validation result.
func orderingIssues(result *Result) []*Error {
	return result.ByCategory(CategoryNodeOrdering)
}

// TestNodeOrdering_RouterSkipUnguardedInjectWarns reproduces the exact shape
// of the pitch-deck production failure:
//
//	Step 'founder_interview' config evaluation failed:
//	workflow.thread.inject.content: ... CEL evaluation error: no such key: scrape_website
//
// A router can dispatch straight to founder_interview, skipping
// scrape_website — so the unguarded {{nodes.scrape_website.response_text}}
// in the inject content hard-fails at runtime. Static validation must flag it.
func TestNodeOrdering_RouterSkipUnguardedInjectWarns(t *testing.T) {
	workflowYAML := `
name: pitch-deck-shape
entry: [classify]
nodes:
  - id: classify
    type: router
    nodes:
      - id: scrape_website
        description: "Start from scratch."
      - id: founder_interview
        description: "Research done - skip to the interview."
    fallback: scrape_website
  - id: scrape_website
    type: run
    command: "echo scrape"
  - id: founder_interview
    type: workflow
    ref: builtin://agent
    thread:
      mode: fork
      inject:
        role: user
        content: |
          ## Founder Interview
          WEBSITE ANALYSIS:
          {{nodes.scrape_website.response_text}}
edges:
  - from: scrape_website
    default: founder_interview
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	issues := orderingIssues(result)
	require.NotEmpty(t, issues,
		"expected a node_ordering finding for unguarded nodes.scrape_website in founder_interview inject content")

	found := false
	for _, issue := range issues {
		if strings.Contains(issue.Message, "scrape_website") &&
			strings.Contains(strings.Join(issue.Path, "."), "founder_interview") &&
			strings.Contains(strings.Join(issue.Path, "."), "inject") {
			found = true
			assert.Equal(t, SeverityWarning, issue.Severity,
				"router-skippable upstream reference should be a warning (node exists and may have run)")
			assert.Contains(t, issue.Message, "no such key",
				"message should explain the runtime failure mode")
			assert.Contains(t, issue.Suggestion, "has(nodes.scrape_website)",
				"suggestion should show the has() guard")
		}
	}
	assert.True(t, found, "expected finding at founder_interview thread.inject.content; got: %v", issues)
}

// TestNodeOrdering_GuardedInjectDoesNotWarn is the fixed version of the
// founder_interview shape: guarding the reference with has() silences the
// ordering warning (mirrors the actual pitch-deck.yaml fix).
func TestNodeOrdering_GuardedInjectDoesNotWarn(t *testing.T) {
	workflowYAML := `
name: pitch-deck-shape-fixed
entry: [classify]
nodes:
  - id: classify
    type: router
    nodes:
      - id: scrape_website
        description: "Start from scratch."
      - id: founder_interview
        description: "Research done - skip to the interview."
    fallback: scrape_website
  - id: scrape_website
    type: run
    command: "echo scrape"
  - id: founder_interview
    type: workflow
    ref: builtin://agent
    thread:
      mode: fork
      inject:
        role: user
        content: |
          ## Founder Interview
          WEBSITE ANALYSIS:
          {{has(nodes.scrape_website) && has(nodes.scrape_website.response_text) ? nodes.scrape_website.response_text : '(not run this session)'}}
edges:
  - from: scrape_website
    default: founder_interview
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	assert.Empty(t, orderingIssues(result),
		"guarded reference must not produce ordering findings: %v", orderingIssues(result))
}

// TestNodeOrdering_UnknownNodeReferenceInInjectIsError verifies the companion
// hard guarantee: a nodes.<id> reference to a node that does not exist in the
// workflow at all is a validation ERROR — including inside thread.inject
// content, not just primary args.
func TestNodeOrdering_UnknownNodeReferenceInInjectIsError(t *testing.T) {
	workflowYAML := `
name: unknown-node-ref
entry: [first]
nodes:
  - id: first
    type: run
    command: "echo hi"
  - id: second
    type: workflow
    ref: builtin://agent
    thread:
      mode: fork
      inject:
        role: user
        content: |
          {{nodes.does_not_exist.response_text}}
edges:
  - from: first
    default: second
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	require.True(t, result.HasErrors(),
		"unknown node reference in inject content must be a hard validation error")
	assert.Contains(t, result.Error(), "does_not_exist")
}

// TestNodeOrdering_DownstreamReferenceIsError: referencing a node that always
// executes AFTER the current node can never be satisfied — hard error.
func TestNodeOrdering_DownstreamReferenceIsError(t *testing.T) {
	workflowYAML := `
name: downstream-ref
entry: [first]
nodes:
  - id: first
    type: call_llm
    model:
      id: test-model
    system_prompt: "Summarize: {{nodes.second.response_text}}"
  - id: second
    type: run
    command: "echo hi"
edges:
  - from: first
    default: second
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	issues := orderingIssues(result)
	require.NotEmpty(t, issues, "expected ordering finding for downstream reference")

	found := false
	for _, issue := range issues {
		if strings.Contains(issue.Message, "'second'") && issue.Severity == SeverityError {
			found = true
			assert.Contains(t, issue.Message, "AFTER")
		}
	}
	assert.True(t, found, "downstream reference must be an ERROR: %v", issues)
}

// TestNodeOrdering_LinearChainDoesNotWarn: straight-line references to
// guaranteed-upstream nodes are fine.
func TestNodeOrdering_LinearChainDoesNotWarn(t *testing.T) {
	workflowYAML := `
name: linear-chain
entry: [first]
nodes:
  - id: first
    type: run
    command: "echo hi"
  - id: second
    type: call_llm
    model:
      id: test-model
    system_prompt: "Result was: {{nodes.first.stdout}}"
edges:
  - from: first
    default: second
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	assert.Empty(t, orderingIssues(result),
		"linear upstream reference must not warn: %v", orderingIssues(result))
}

// TestNodeOrdering_AllJoinGuaranteesBothBranches: after an "all" join, BOTH
// parallel parents are guaranteed complete — no warning.
func TestNodeOrdering_AllJoinGuaranteesBothBranches(t *testing.T) {
	workflowYAML := `
name: all-join
entry: [start]
nodes:
  - id: start
    type: run
    command: "echo start"
  - id: branch_a
    type: run
    command: "echo a"
  - id: branch_b
    type: run
    command: "echo b"
  - id: joined
    type: join
    condition: "all"
  - id: consumer
    type: call_llm
    model:
      id: test-model
    system_prompt: "A: {{nodes.branch_a.stdout}} B: {{nodes.branch_b.stdout}}"
edges:
  - from: start
    default: branch_a
  - from: start
    default: branch_b
  - from: branch_a
    default: joined
  - from: branch_b
    default: joined
  - from: joined
    default: consumer
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	assert.Empty(t, orderingIssues(result),
		"all-join guarantees both branches — no ordering findings expected: %v", orderingIssues(result))
}

// TestNodeOrdering_ParallelSiblingWarns: a node referencing its parallel
// sibling (no join in between) is not guaranteed ordering — warning.
func TestNodeOrdering_ParallelSiblingWarns(t *testing.T) {
	workflowYAML := `
name: parallel-sibling
entry: [start]
nodes:
  - id: start
    type: run
    command: "echo start"
  - id: branch_a
    type: run
    command: "echo a"
  - id: branch_b
    type: call_llm
    model:
      id: test-model
    system_prompt: "Sibling said: {{nodes.branch_a.stdout}}"
edges:
  - from: start
    default: branch_a
  - from: start
    default: branch_b
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	issues := orderingIssues(result)
	require.NotEmpty(t, issues, "parallel sibling reference should warn")
	assert.Equal(t, SeverityWarning, issues[0].Severity)
}

// TestNodeOrdering_ConditionalNodeCoveredByConditionalWarning: nodes with
// their own condition are excluded from ordering findings — the existing
// conditional-access warning covers them (their output KEY exists even when
// skipped, only field access is risky).
func TestNodeOrdering_ConditionalNodeCoveredByConditionalWarning(t *testing.T) {
	workflowYAML := `
name: conditional-node
entry: [first]
inputs:
  run_lint:
    type: boolean
    default: true
nodes:
  - id: first
    type: run
    command: "echo hi"
  - id: lint
    type: run
    condition: "inputs.run_lint"
    command: "echo lint"
  - id: summary
    type: call_llm
    model:
      id: test-model
    system_prompt: "Lint: {{nodes.lint.stdout}}"
edges:
  - from: first
    default: lint
  - from: lint
    default: summary
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	assert.Empty(t, orderingIssues(result),
		"conditional nodes are covered by conditional_access, not node_ordering: %v", orderingIssues(result))
	assert.NotEmpty(t, result.ByCategory(CategoryConditionalAccess),
		"the conditional-access warning should still fire for unguarded conditional node access")
}

// TestComputeGuaranteedBefore_PitchDeckShape verifies the guaranteed-before
// sets on the pitch-deck-like graph directly.
func TestComputeGuaranteedBefore_PitchDeckShape(t *testing.T) {
	workflowYAML := `
name: guaranteed-before
entry: [classify]
nodes:
  - id: classify
    type: router
    nodes:
      - id: scrape
        description: "start"
      - id: interview
        description: "skip ahead"
    fallback: scrape
  - id: scrape
    type: run
    command: "echo scrape"
  - id: research_a
    type: run
    command: "echo a"
  - id: research_b
    type: run
    command: "echo b"
  - id: research_done
    type: join
    condition: "all"
  - id: interview
    type: run
    command: "echo interview"
edges:
  - from: scrape
    default: research_a
  - from: scrape
    default: research_b
  - from: research_a
    default: research_done
  - from: research_b
    default: research_done
  - from: research_done
    default: interview
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	guaranteed := computeGuaranteedBefore(wf)

	// Entry: nothing guaranteed before classify.
	assert.Empty(t, sortedNodeIDs(guaranteed["classify"]))

	// scrape only reachable via classify.
	assert.Equal(t, []string{"classify"}, sortedNodeIDs(guaranteed["scrape"]))

	// research_a: only via scrape.
	assert.Equal(t, []string{"classify", "scrape"}, sortedNodeIDs(guaranteed["research_a"]))

	// research_done ("all" join): both branches guaranteed.
	assert.Equal(t, []string{"classify", "research_a", "research_b", "scrape"},
		sortedNodeIDs(guaranteed["research_done"]))

	// interview: reachable via research_done AND directly via the router —
	// only classify is guaranteed (the production bug's exact shape).
	assert.Equal(t, []string{"classify"}, sortedNodeIDs(guaranteed["interview"]))
}
