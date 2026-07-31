// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// A run that routes to its workflow's failure terminal ends its graph with no
// error at all — the Temporal execution completes. On run e10cabae that meant
// the run reported COMPLETED to every supervision surface while it had failed
// every gate lane five times and produced no files. The verdict has to be a
// fact the run RECORDS, not one a reader infers from the lifecycle.

// terminalOutcomeYAML routes to a terminal node that declares the run failed,
// exactly the shape forge-one-shot uses (`- id: failed` reached when a phase
// never went green).
const terminalOutcomeYAML = `
name: resume-test
entry: [work]
nodes:
  - id: work
    type: call_llm
  - id: failed
    type: call_llm
    outcome: failure
edges:
  - from: work
    default: failed
`

const successOutcomeYAML = `
name: resume-test
entry: [work]
nodes:
  - id: work
    type: call_llm
  - id: success
    type: call_llm
    outcome: success
edges:
  - from: work
    default: success
`

const noOutcomeYAML = `
name: resume-test
entry: [work]
nodes:
  - id: work
    type: call_llm
`

// terminalStatus returns the last WorkflowStatus notification the run emitted —
// the one that records how it ended.
func terminalStatus(t *testing.T, rec *resumeEnvRecorder) map[string]interface{} {
	t.Helper()
	require.NotEmpty(t, rec.statuses, "the run emitted no status notifications at all")
	return rec.statuses[len(rec.statuses)-1]
}

func TestDynamicWorkflow_FailureTerminalRecordsTheVerdict(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, terminalOutcomeYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-outcome-fail", nil))

	// The graph really did run to its end — this is NOT an error path.
	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"work", "failed"}, rec.executedNodes)

	final := terminalStatus(t, rec)
	assert.Equal(t, "completed", final["status"],
		"the lifecycle is completed — the Temporal execution finished, and pretending otherwise would break resume routing")
	assert.Equal(t, model.OutcomeFailure, final["outcome"],
		"a run that reached its failure terminal must record the verdict; without it every surface reads COMPLETED and nothing else")
}

func TestDynamicWorkflow_SuccessTerminalRecordsTheVerdict(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, successOutcomeYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-outcome-pass", nil))

	require.NoError(t, env.GetWorkflowError())
	final := terminalStatus(t, rec)
	assert.Equal(t, "completed", final["status"])
	assert.Equal(t, model.OutcomeSuccess, final["outcome"])
}

// TestDynamicWorkflow_UndeclaredOutcomeStaysUndeclared: most workflows declare
// nothing, and absence must never be recorded as a verdict in either direction.
func TestDynamicWorkflow_UndeclaredOutcomeStaysUndeclared(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, noOutcomeYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-outcome-none", nil))

	require.NoError(t, env.GetWorkflowError())
	final := terminalStatus(t, rec)
	assert.Equal(t, "completed", final["status"])
	_, declared := final["outcome"]
	assert.False(t, declared, "a workflow that declares no outcome must record none, not an invented one")
}

// TestDynamicWorkflow_SkippedTerminalStampsNothing: a node whose condition is
// false never runs, so it must not stamp its verdict on the run.
func TestDynamicWorkflow_SkippedTerminalStampsNothing(t *testing.T) {
	const skippedYAML = `
name: resume-test
entry: [work]
nodes:
  - id: work
    type: call_llm
  - id: failed
    type: call_llm
    condition: "false"
    outcome: failure
edges:
  - from: work
    default: failed
`
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, skippedYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-outcome-skipped", nil))

	require.NoError(t, env.GetWorkflowError())
	assert.NotContains(t, rec.executedNodes, "failed")
	final := terminalStatus(t, rec)
	_, declared := final["outcome"]
	assert.False(t, declared, "a skipped node must not stamp its outcome on the run")
}
