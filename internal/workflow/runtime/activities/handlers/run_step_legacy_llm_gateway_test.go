// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/rowreads"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// An ExecuteRunStep a release with llm_gateway scheduled (its payload, as
// Temporal hands it to this worker) is refused, never run without the step
// key it was declared with.
func TestRunStep_RemovedLLMGatewayIsRefusedWithoutRunningTheCommand(t *testing.T) {
	t.Parallel()

	var input ExecuteRunStepInput
	require.NoError(t, json.Unmarshal([]byte(`{"workflow_id":"wf","step_id":"bench","command":"claude -p hi",
		"node_path":"bench","llm_gateway":{"cli":"claude-code","budget_usd":2}}`), &input))

	repo := setupTestRepo(t)
	input.ChatID = createTestChat(t, repo)
	mock := &mockRunExecutor{responses: []mockRunResponse{{exitCode: 0}}}
	activity := NewExecuteRunStepActivity(rowreads.WithRepo(repo), workspaceRowsOf(repo), nil, mock)

	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)
	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)
	var out ExecuteRunStepOutput
	require.NoError(t, val.Get(&out))

	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Stderr, "llm_gateway, which was removed")
	assert.Empty(t, mock.calls, "a command declared with llm_gateway must never run without its step key")
}

func TestHasLegacyLLMGateway(t *testing.T) {
	t.Parallel()
	assert.True(t, hasLegacyLLMGateway(json.RawMessage(`{"cli":"claude-code"}`)))
	assert.False(t, hasLegacyLLMGateway(nil))
	assert.False(t, hasLegacyLLMGateway(json.RawMessage(`null`)))
}
