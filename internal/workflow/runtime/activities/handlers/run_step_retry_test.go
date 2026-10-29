// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

func runStepWith(t *testing.T, responses ...mockRunResponse) (*mockRunExecutor, ExecuteRunStepOutput, error) {
	t.Helper()
	repo := setupTestRepo(t)
	chatID := createTestChat(t, repo)
	mock := &mockRunExecutor{responses: responses}
	act := NewExecuteRunStepActivity(repo, nil, mock)
	act.undeliveredRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}

	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(act.Execute)
	val, err := env.ExecuteActivity(act.Execute, ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: chatID, StepID: "gate", Command: "task ci",
	})
	var out ExecuteRunStepOutput
	if err == nil {
		require.NoError(t, val.Get(&out))
	}
	return mock, out, err
}

func notDispatchedErr() error {
	return &toolexec.TransportError{Code: toolexec.ErrorCodeDaemonUnreached, Command: "task ci",
		Detail: "no daemon connected for user", NotDispatched: true}
}

func requireNonRetryable(t *testing.T, err error, errType string) {
	t.Helper()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "an application error: %v", err)
	assert.Equal(t, errType, appErr.Type())
	assert.True(t, appErr.NonRetryable(),
		"a Temporal retry could only refuse: it cannot tell how the attempt before it ended")
}

// A command that provably never reached a machine is sent again, within the
// attempt that knows it never ran, and its verdict is the step's.
func TestRunStep_NeverDispatchedCommandIsSentAgain(t *testing.T) {
	t.Parallel()

	mock, out, err := runStepWith(t,
		mockRunResponse{exitCode: -1, err: notDispatchedErr()},
		mockRunResponse{exitCode: -1, err: notDispatchedErr()},
		mockRunResponse{stdout: "ok", exitCode: 0},
	)

	require.NoError(t, err)
	assert.Equal(t, "ok", out.Stdout)
	assert.Len(t, mock.calls, 3, "sent until it reached the machine")
}

// A machine that stays unreachable fails the step after the ladder, saying
// the command never ran, and without a retry that would only refuse.
func TestRunStep_UnreachableMachineFailsWithoutRetries(t *testing.T) {
	t.Parallel()

	mock, _, err := runStepWith(t, mockRunResponse{exitCode: -1, err: notDispatchedErr()})

	requireNonRetryable(t, err, runStepNotDispatchedErrType)
	assert.Contains(t, err.Error(), "the command never ran")
	assert.Len(t, mock.calls, 1+len(defaultUndeliveredRetryDelays))
}

// A command that was sent and whose result never came back may have run —
// prod's 65 cases were 5m gates that ran into their timeout. It is never sent
// again, and the failure says it may have run rather than that it never did.
func TestRunStep_CommandThatMayHaveRunIsSentOnce(t *testing.T) {
	t.Parallel()

	for name, transportErr := range map[string]*toolexec.TransportError{
		"gateway timed out": {Code: toolexec.ErrorCodeDaemonRoundTrip, Command: "task ci",
			Detail: `tool request "258035a7" got no answer from the machine in 5m20s; it may have run`},
		"connection dropped": {Code: toolexec.ErrorCodeDaemonInterrupted, Command: "task ci",
			Detail: "daemon connection interrupted"},
		"wait cancelled": {Code: toolexec.ErrorCodeDaemonUnreached, Command: "task ci",
			Detail: "tool request via NATS failed: context canceled"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, _, err := runStepWith(t, mockRunResponse{exitCode: -1, err: transportErr})

			requireNonRetryable(t, err, runStepOutcomeUnknownErrType)
			assert.Contains(t, err.Error(), "may have run")
			assert.NotContains(t, err.Error(), "never ran")
			assert.Len(t, mock.calls, 1, "a command that may have run is never sent again")
		})
	}
}
