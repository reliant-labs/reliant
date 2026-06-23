// Copyright (c) 2025 Reliant Labs
package analysis

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func TestAnalyzeWorkflowSequentialAgentsRecommendParallelization(t *testing.T) {
	workflow := mustParseWorkflow(t, `
name: sequential-agents
entry: [scaffold]
inputs:
  ask:
    type: boolean
    default: false
nodes:
  - id: scaffold
    type: workflow
    ref: builtin://agent
  - id: frontend
    type: workflow
    ref: builtin://agent
  - id: backend
    type: workflow
    ref: builtin://agent
edges:
  - from: scaffold
    default: frontend
  - from: frontend
    default: backend
`)

	report := AnalyzeWorkflow(workflow, testOptions())

	require.Equal(t, "pass", report.Status)
	require.Equal(t, 3, report.Complexity.AgentNodes)
	require.Equal(t, 180, report.Speed.EstimatedCriticalPathSeconds)
	require.Equal(t, []string{"scaffold", "frontend", "backend"}, report.Speed.CriticalPath)
	requireRecommendation(t, report, "parallelize_sequential_agents")
	requireRecommendation(t, report, "consider_parallel_edge")
}

func TestAnalyzeWorkflowForkJoinUsesCriticalPathNotSerialSum(t *testing.T) {
	workflow := mustParseWorkflow(t, `
name: fork-join
entry: [start]
nodes:
  - id: start
    type: save_message
    role: assistant
    content: start
  - id: frontend
    type: workflow
    ref: builtin://agent
  - id: backend
    type: workflow
    ref: builtin://agent
  - id: join
    type: join
  - id: done
    type: save_message
    role: assistant
    content: done
edges:
  - from: start
    default: [frontend, backend]
  - from: frontend
    default: join
  - from: backend
    default: join
  - from: join
    default: done
`)

	report := AnalyzeWorkflow(workflow, testOptions())

	require.Equal(t, 122, report.Speed.EstimatedSerialSeconds)
	require.Equal(t, 62, report.Speed.EstimatedCriticalPathSeconds)
	require.Greater(t, report.Speed.ParallelismRatio, 1.9)
	require.Len(t, report.Speed.CriticalPath, 4)
}

func TestAnalyzeWorkflowLoopWithInputBoundedRetries(t *testing.T) {
	workflow := mustParseWorkflow(t, `
name: bounded-loop
entry: [attempt]
inputs:
  max_retries:
    type: integer
    default: 3
nodes:
  - id: attempt
    type: loop
    while: outputs.strategy != 'pass' && iter.iteration < inputs.max_retries
    inline:
      entry: [implement]
      nodes:
        - id: implement
          type: workflow
          ref: builtin://agent
`)

	report := AnalyzeWorkflow(workflow, testOptions())

	require.Equal(t, 1, report.Complexity.LoopNodes)
	require.Equal(t, 0, report.Complexity.UnboundedLoops)
	require.Equal(t, 181, report.Speed.EstimatedCriticalPathSeconds)
	require.Empty(t, warningsWithCode(report, "unbounded_loop"))
}

func TestAnalyzeWorkflowUnboundedNestedAgentLoopWarns(t *testing.T) {
	workflow := mustParseWorkflow(t, `
name: unbounded-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: outputs.tool_calls != null
    inline:
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          tools_config:
            filter: [tag:default]
            spawn: [spawn:builtin://agent]
        - id: execute_tools
          type: execute_tools
      edges:
        - from: call_llm
          default: execute_tools
`)

	report := AnalyzeWorkflow(workflow, testOptions())

	require.Equal(t, 1, report.Complexity.UnboundedLoops)
	require.Equal(t, 1, report.Complexity.SpawnEnabledAgents)
	require.Equal(t, 1, report.Complexity.BroadToolNodes)
	require.Equal(t, 221, report.Speed.EstimatedCriticalPathSeconds)
	requireWarning(t, report, "unbounded_loop")
	requireRecommendation(t, report, "add_natural_checkpoints")
}

func TestAnalyzeWorkflowParallelLoopEstimatesCriticalPathOnce(t *testing.T) {
	workflow := mustParseWorkflow(t, `
name: parallel-loop
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "[ux, security, code]"
    inline:
      entry: [review]
      nodes:
        - id: review
          type: workflow
          ref: builtin://structured-agent
`)

	report := AnalyzeWorkflow(workflow, testOptions())

	require.Equal(t, 1, report.Complexity.ParallelLoops)
	require.Equal(t, 91, report.Speed.EstimatedSerialSeconds)
	require.Equal(t, 31, report.Speed.EstimatedCriticalPathSeconds)
	require.Equal(t, 2.94, report.Speed.ParallelismRatio)
}

func TestAnalyzeWorkflowLoadsReferencedSubWorkflow(t *testing.T) {
	childWorkflow := mustParseWorkflow(t, `
name: child
entry: [loop]
nodes:
  - id: loop
    type: loop
    while: iter.iteration < 2
    inline:
      entry: [call]
      nodes:
        - id: call
          type: call_llm
`)
	parentWorkflow := mustParseWorkflow(t, `
name: parent
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: builtin://child
`)
	options := testOptions()
	options.WorkflowLoader = func(workflowRef string) (*reliantv1.Workflow, error) {
		require.Equal(t, "builtin://child", workflowRef)
		return childWorkflow, nil
	}

	report := AnalyzeWorkflow(parentWorkflow, options)

	require.Equal(t, 1, report.Complexity.ReferencedWorkflows)
	require.Equal(t, 1, report.Complexity.AgentNodes)
	require.Equal(t, 1, report.Complexity.LoopNodes)
	require.Equal(t, 42, report.Speed.EstimatedCriticalPathSeconds)
	require.Equal(t, []string{"child"}, report.Speed.CriticalPath)
	require.Equal(t, "builtin://child", report.Nodes[0].ReferencedWorkflow)
}

func testOptions() Options {
	options := DefaultOptions()
	options.CallLLMSeconds = 20
	options.RunSeconds = 5
	options.AgentSeconds = 60
	options.StructuredSeconds = 30
	options.ExecuteToolsSeconds = 2
	options.ActivitySeconds = 1
	options.UnboundedLoopIterations = 10
	options.ParallelLoopItems = 3
	return options
}

func mustParseWorkflow(t *testing.T, content string) *reliantv1.Workflow {
	t.Helper()
	workflow, err := wfyaml.ParseWorkflow([]byte(strings.TrimSpace(content)))
	require.NoError(t, err)
	return workflow
}

func requireRecommendation(t *testing.T, report Report, code string) {
	t.Helper()
	for _, recommendation := range report.Recommendations {
		if recommendation.Code == code {
			return
		}
	}
	t.Fatalf("expected recommendation %q in %#v", code, report.Recommendations)
}

func requireWarning(t *testing.T, report Report, code string) {
	t.Helper()
	require.NotEmpty(t, warningsWithCode(report, code), "expected warning %q", code)
}

func warningsWithCode(report Report, code string) []Warning {
	var matches []Warning
	for _, warning := range report.Warnings {
		if warning.Code == code {
			matches = append(matches, warning)
		}
	}
	return matches
}
