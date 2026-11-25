// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"
)

// cliLoginOnlyJSON is what ActivityLoadWorkflow recorded (protojson) for a
// workflow whose only requirement was requires.cli_login, before its removal.
const cliLoginOnlyJSON = `{"name":"bench","requires":{"cliLogin":["claude"]}}`

// legacyRequirementsWorkflow is DynamicWorkflow's startup slice for a
// definition recorded with requires.cli_login: the removed key, read from the
// recorded definition, then the requirements check, then the first node.
func legacyRequirementsWorkflow(ctx workflow.Context, workflowJSON string) error {
	wf, err := LoadWorkflow([]byte(workflowJSON))
	if err != nil {
		return err
	}
	execCtx := &ExecutionContext{}
	input := WorkflowInput{ChatID: "c1", WorkflowName: wf.GetName()}
	legacy := legacyRequiresCLILogin(ctx, []byte(workflowJSON), execCtx)
	if err := checkRequirements(ctx, input, execCtx, wf, "wf1", "wf1", true, legacy); err != nil {
		return err
	}
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10e9}), "FirstNode").Get(ctx, nil)
}

// A run recorded while requires.cli_login existed (no cliLoginRemovedChangeID
// marker) scheduled PreflightRequirements for it, and still does, so its
// history replays.
func TestRecordedCLILoginKeepsSchedulingThePreflight(t *testing.T) {
	env := newRequirementsEnv()
	env.RegisterWorkflow(legacyRequirementsWorkflow)
	env.OnGetVersion(cliLoginRemovedChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).Return(map[string]interface{}{}, nil).Once()
	env.OnActivity("FirstNode", mock.Anything).Return(map[string]interface{}{}, nil).Once()
	env.ExecuteWorkflow(legacyRequirementsWorkflow, cliLoginOnlyJSON)
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A run that reaches the check live records the marker and checks nothing
// for cli_login: with nothing else required, no preflight is scheduled.
func TestCLILoginIsNotCheckedOnANewRun(t *testing.T) {
	env := newRequirementsEnv()
	env.RegisterWorkflow(legacyRequirementsWorkflow)
	preflights := 0
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).
		Run(func(mock.Arguments) { preflights++ }).Return(map[string]interface{}{}, nil).Maybe()
	env.OnActivity("FirstNode", mock.Anything).Return(map[string]interface{}{}, nil).Once()
	env.ExecuteWorkflow(legacyRequirementsWorkflow, cliLoginOnlyJSON)
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	assert.Zero(t, preflights, "a new run must not check the removed cli_login")
}

func TestRecordedCLILogin(t *testing.T) {
	assert.Equal(t, []string{"claude"}, recordedCLILogin([]byte(cliLoginOnlyJSON)))
	assert.Equal(t, []string{"codex"}, recordedCLILogin([]byte(`{"requires":{"cli_login":["codex"]}}`)))
	assert.Nil(t, recordedCLILogin([]byte(`{"requires":{"tools":["git"]}}`)))
	assert.Nil(t, recordedCLILogin(nil))
	assert.Nil(t, recordedCLILogin([]byte(`not json`)))
}

// A definition recorded with the removed keys parses again once they are
// dropped.
func TestRecordedDefinitionWithRemovedKeysStillParses(t *testing.T) {
	recorded := []byte(`name: bench
requires:
  tools: [git]
  cli_login: [claude]
entry: bench
nodes:
  - id: bench
    type: run
    args:
      command: claude -p "{{inputs.task}}"
      llm_gateway: {cli: claude-code, budget_usd: 2}
`)
	wf, err := ResolveAndParseWorkflow(recorded, map[string]interface{}{"task": "fix it"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"git"}, wf.GetRequires().GetTools())
	require.Len(t, wf.GetNodes(), 1)
}

// llm_gateway is dropped from every run node, however deeply nested, and
// from nothing else.
func TestDropRunLLMGatewayReachesNestedNodes(t *testing.T) {
	inner := map[string]interface{}{"id": "inner", "type": "run", "args": map[string]interface{}{"command": "x", "llm_gateway": map[string]interface{}{"cli": "claude-code"}}}
	sugared := map[string]interface{}{"id": "s", "type": "run", "command": "x", "llm_gateway": map[string]interface{}{"cli": "claude-code"}}
	other := map[string]interface{}{"id": "a", "type": "agent", "args": map[string]interface{}{"llm_gateway": "kept"}}
	raw := map[string]interface{}{
		"requires": map[string]interface{}{"tools": []interface{}{"git"}, "cli_login": []interface{}{"claude"}},
		"nodes": []interface{}{
			map[string]interface{}{"id": "each", "type": "loop", "args": map[string]interface{}{"inline": map[string]interface{}{"nodes": []interface{}{inner}}}},
			sugared,
			other,
		},
	}
	dropRemovedDefinitionKeys(raw)
	assert.NotContains(t, raw["requires"], "cli_login")
	assert.Contains(t, raw["requires"], "tools")
	assert.NotContains(t, inner["args"], "llm_gateway")
	assert.Contains(t, inner["args"], "command")
	assert.NotContains(t, sugared, "llm_gateway")
	assert.Contains(t, sugared, "command")
	assert.Contains(t, other["args"], "llm_gateway")
}

func TestRecordedLLMGatewayNodes(t *testing.T) {
	// The shape ActivityLoadWorkflow records: protojson, nested graphs included.
	nodes := recordedLLMGatewayNodes([]byte(`{"name":"b","nodes":[
		{"id":"bench","type":"run","run":{"command":{"expr":"x"},"llmGateway":{"cli":"claude-code","budgetUsd":2}}},
		{"id":"plain","type":"run","run":{"command":{"expr":"y"}}},
		{"id":"each","type":"loop","loop":{"inline":{"nodes":[{"id":"inner","type":"run","run":{"llmGateway":{"cli":"claude-code"}}}]}}}]}`))
	assert.Len(t, nodes, 2)
	assert.JSONEq(t, `{"cli":"claude-code","budgetUsd":2}`, string(nodes["bench"]))
	assert.Contains(t, nodes, "inner")
	assert.Nil(t, recordedLLMGatewayNodes([]byte(`{"nodes":[{"id":"a","type":"run","run":{}}]}`)))
	assert.Nil(t, recordedLLMGatewayNodes(nil))
}

// The recorded nodes ride the workflow context to the step executor, merged
// with an enclosing definition's.
func TestLegacyLLMGatewayRidesTheContext(t *testing.T) {
	probe := func(ctx workflow.Context) ([]string, error) {
		ctx = withLegacyLLMGateway(ctx, map[string]json.RawMessage{"outer": json.RawMessage(`{}`)})
		ctx = withLegacyLLMGateway(ctx, map[string]json.RawMessage{"inner": json.RawMessage(`{}`)})
		ctx = withLegacyLLMGateway(ctx, nil)
		var found []string
		for _, id := range []string{"outer", "inner", "other"} {
			if _, ok := legacyLLMGateway(ctx, id); ok {
				found = append(found, id)
			}
		}
		return found, nil
	}
	env := newRequirementsEnv()
	env.RegisterWorkflow(probe)
	env.ExecuteWorkflow(probe)
	require.NoError(t, env.GetWorkflowError())
	var found []string
	require.NoError(t, env.GetWorkflowResult(&found))
	assert.Equal(t, []string{"outer", "inner"}, found)
}
