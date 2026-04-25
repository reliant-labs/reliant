// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// mockRunExecutor captures all commands sent and returns configured responses.
type mockRunExecutor struct {
	calls     []mockRunCall
	responses []mockRunResponse // responses returned in order; last one repeats
}

type mockRunCall struct {
	command    string
	workingDir string
	timeoutMs  int
	env        map[string]string
}

type mockRunResponse struct {
	stdout      string
	stderr      string
	exitCode    int
	interrupted bool
	err         error
}

func (m *mockRunExecutor) ExecuteCommand(
	ctx context.Context,
	command string,
	workingDir string,
	timeoutMs int,
	env map[string]string,
) (string, string, int, bool, error) {
	m.calls = append(m.calls, mockRunCall{
		command:    command,
		workingDir: workingDir,
		timeoutMs:  timeoutMs,
		env:        env,
	})

	idx := len(m.calls) - 1
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	if idx < 0 {
		return "", "", 0, false, nil
	}
	r := m.responses[idx]
	return r.stdout, r.stderr, r.exitCode, r.interrupted, r.err
}

func TestRunStep_LogFile_WritesOutput(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	mock := &mockRunExecutor{
		responses: []mockRunResponse{
			{stdout: "hello world", stderr: "some warning", exitCode: 0},
			{exitCode: 0}, // log file write command
		},
	}

	activity := NewExecuteRunStepActivity(repo, nil, mock)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	input := ExecuteRunStepInput{
		WorkflowID: "wf-1",
		ChatID:     chatID,
		StepID:     "step-1",
		Command:    "echo hello world",
		LogFile:    "/tmp/test-logs/output.log",
	}

	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)

	var output ExecuteRunStepOutput
	require.NoError(t, val.Get(&output))

	// Verify the command was executed
	require.Len(t, mock.calls, 2, "expected 2 calls: command + log file write")
	assert.Equal(t, "echo hello world", mock.calls[0].command)

	// Verify log file write command creates parent dirs and writes content
	writeCmd := mock.calls[1].command
	assert.Contains(t, writeCmd, "mkdir -p")
	assert.Contains(t, writeCmd, "/tmp/test-logs")
	assert.Contains(t, writeCmd, "/tmp/test-logs/output.log")
	assert.Contains(t, writeCmd, "hello world")
	assert.Contains(t, writeCmd, "some warning")

	// Verify output fields
	assert.Equal(t, 0, output.ExitCode)
	assert.Equal(t, "hello world", output.Stdout)
	assert.Equal(t, "some warning", output.Stderr)
	assert.Equal(t, "/tmp/test-logs/output.log", output.LogFile)
}

func TestRunStep_LogFile_Empty_NoFileWrite(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	mock := &mockRunExecutor{
		responses: []mockRunResponse{
			{stdout: "output", exitCode: 0},
		},
	}

	activity := NewExecuteRunStepActivity(repo, nil, mock)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	input := ExecuteRunStepInput{
		WorkflowID: "wf-1",
		ChatID:     chatID,
		StepID:     "step-1",
		Command:    "echo output",
		// LogFile intentionally empty
	}

	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)

	var output ExecuteRunStepOutput
	require.NoError(t, val.Get(&output))

	// Only one call — no log file write
	require.Len(t, mock.calls, 1, "expected only 1 call when log_file is empty")
	assert.Equal(t, "", output.LogFile)
}

func TestRunStep_LogFile_ExitCodePreserved(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	mock := &mockRunExecutor{
		responses: []mockRunResponse{
			{stdout: "error output", exitCode: 42},
			{exitCode: 0}, // log file write succeeds
		},
	}

	activity := NewExecuteRunStepActivity(repo, nil, mock)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	input := ExecuteRunStepInput{
		WorkflowID: "wf-1",
		ChatID:     chatID,
		StepID:     "step-1",
		Command:    "failing-command",
		LogFile:    "/tmp/test.log",
	}

	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)

	var output ExecuteRunStepOutput
	require.NoError(t, val.Get(&output))

	// Exit code from the original command must be preserved
	assert.Equal(t, 42, output.ExitCode)
	assert.Equal(t, "/tmp/test.log", output.LogFile)
}

func TestRunStep_LogFile_RelativePath(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	mock := &mockRunExecutor{
		responses: []mockRunResponse{
			{stdout: "data", exitCode: 0},
			{exitCode: 0},
		},
	}

	activity := NewExecuteRunStepActivity(repo, nil, mock)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	input := ExecuteRunStepInput{
		WorkflowID: "wf-1",
		ChatID:     chatID,
		StepID:     "step-1",
		Command:    "echo data",
		LogFile:    "logs/output.log", // relative path
	}

	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)

	var output ExecuteRunStepOutput
	require.NoError(t, val.Get(&output))

	// Relative path should be joined with working dir (project path from test = /tmp/test)
	assert.True(t, strings.HasPrefix(output.LogFile, "/tmp/test"), "expected absolute path starting with working dir, got: %s", output.LogFile)
	assert.True(t, strings.HasSuffix(output.LogFile, "logs/output.log"), "expected path ending with logs/output.log, got: %s", output.LogFile)
}

func TestRunStep_LogFile_WriteFailure_GracefulDegradation(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)

	mock := &mockRunExecutor{
		responses: []mockRunResponse{
			{stdout: "success output", exitCode: 0},
			{exitCode: 1, stderr: "permission denied", err: nil}, // write cmd fails but no Go error
		},
	}

	// The write command itself doesn't return a Go error (it's just a non-zero exit code),
	// so the activity won't consider it a failure. The file write via executor.ExecuteCommand
	// only reports a Go error for transport-level failures. For this test we verify
	// that the overall step still succeeds.
	activity := NewExecuteRunStepActivity(repo, nil, mock)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	input := ExecuteRunStepInput{
		WorkflowID: "wf-1",
		ChatID:     chatID,
		StepID:     "step-1",
		Command:    "echo success output",
		LogFile:    "/readonly/dir/output.log",
	}

	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)

	var output ExecuteRunStepOutput
	require.NoError(t, val.Get(&output))

	// Step should succeed even if log file write had issues
	assert.Equal(t, 0, output.ExitCode)
	assert.Equal(t, "success output", output.Stdout)
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"with'quote", "'with'\"'\"'quote'"},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, shellQuote(tt.input))
		})
	}
}
