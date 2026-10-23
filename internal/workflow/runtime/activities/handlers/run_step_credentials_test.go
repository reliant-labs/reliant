// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

type capturingToolExecutor struct{ reqs []*toolexec.ToolRequest }

func (e *capturingToolExecutor) ExecuteTool(_ context.Context, r *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	e.reqs = append(e.reqs, r)
	return &toolexec.ToolResult{Content: `{"stdout":"","stderr":"","exit_code":0}`}, nil
}
func (e *capturingToolExecutor) Close() error { return nil }

func shellInputOf(t *testing.T, r *toolexec.ToolRequest) map[string]any {
	t.Helper()
	var in map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.ToolInput), &in))
	return in
}

// A run node's credentials reach the shell tool the daemon executes.
func TestRemoteRunExecutor_CarriesCredentials(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		credentials string
		wantKey     bool
	}{{"none", true}, {"", false}} {
		cap := &capturingToolExecutor{}
		exec := NewRemoteRunExecutor(cap)
		exec.SetContext(RunExecutorContext{UserID: "u", ProjectPath: "/p", Credentials: tc.credentials})
		_, _, _, _, err := exec.ExecuteCommand(context.Background(), "git push", "/p", 1000, nil)
		require.NoError(t, err)
		require.Len(t, cap.reqs, 1)
		in := shellInputOf(t, cap.reqs[0])
		_, has := in["credentials"]
		assert.Equal(t, tc.wantKey, has, "credentials=%q: input %v", tc.credentials, in)
		if tc.wantKey {
			assert.Equal(t, "none", in["credentials"])
		}
	}
}

func TestRunStep_UnknownCredentialsFailsTheStepWithoutRunningTheCommand(t *testing.T) {
	t.Parallel()

	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	mock := &mockRunExecutor{responses: []mockRunResponse{{exitCode: 0}}}
	activity := NewExecuteRunStepActivity(repo, nil, mock)

	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)
	val, err := env.ExecuteActivity(activity.Execute, ExecuteRunStepInput{
		WorkflowID: "wf", ChatID: chatID, StepID: "s", Command: "git push", Credentials: "nne",
	})
	require.NoError(t, err)
	var out ExecuteRunStepOutput
	require.NoError(t, val.Get(&out))

	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Stderr, "credentials")
	assert.Empty(t, mock.calls, "a typo in credentials must never run the command")
}
