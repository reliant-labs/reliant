// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// fixedToolExecutor returns one canned ToolResult for every request.
type fixedToolExecutor struct{ result *toolexec.ToolResult }

func (e *fixedToolExecutor) ExecuteTool(context.Context, *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	return e.result, nil
}

func (e *fixedToolExecutor) Close() error { return nil }

func runOnce(t *testing.T, result *toolexec.ToolResult) (stdout, stderr string, exitCode int, err error) {
	t.Helper()
	exec := NewRemoteRunExecutor(&fixedToolExecutor{result: result})
	exec.SetContext(RunExecutorContext{UserID: "u", ProjectPath: "/tmp/proj"})
	stdout, stderr, exitCode, _, err = exec.ExecuteCommand(
		context.Background(), "reliant forge lint", "/tmp/proj", 300000, nil)
	return stdout, stderr, exitCode, err
}

// A lane that never ran is not a lane that failed.
//
// Both transport failures below were recorded as `exit 1` with empty stdout —
// byte-identical to a real lint failure. The workflow read that as a failed
// gate, burned one of five attempts, and told the agent to redo work that had
// already passed. Two full attempts went that way on 2026-07-27.
func TestTransportFailureIsNotReportedAsACommandFailure(t *testing.T) {
	for name, result := range map[string]*toolexec.ToolResult{
		// The gateway's connection died while the request was in flight.
		"round trip lost": {
			IsError:      true,
			ErrorCode:    toolexec.ErrorCodeDaemonRoundTrip,
			Content:      `daemon disconnected while waiting for tool request "1785..." response`,
			ErrorMessage: "daemon disconnected while waiting for tool request",
		},
		// The router never got an answer at all.
		"daemon unreachable": {
			IsError:      true,
			ErrorCode:    toolexec.ErrorCodeDaemonUnreached,
			Content:      "Failed to execute tool on daemon: tool request via NATS failed: nats: timeout",
			ErrorMessage: "tool request via NATS failed: nats: timeout",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, _, exitCode, err := runOnce(t, result)

			require.Error(t, err, "a transport failure must not be reported as a command verdict")
			var transportErr *toolexec.TransportError
			require.True(t, errors.As(err, &transportErr),
				"callers must be able to tell 'never ran' from 'ran and failed' without scanning a message")
			require.Equal(t, result.ErrorCode, transportErr.Code)
			require.NotEqual(t, 1, exitCode,
				"exit 1 is indistinguishable from a genuine lint/test/build failure")
			require.Empty(t, stdout)
		})
	}
}

// The discriminator must not over-fire: an error the TOOL reported about this
// specific command is a real verdict and must still surface as a failed step.
func TestToolReportedErrorIsStillACommandFailure(t *testing.T) {
	_, stderr, exitCode, err := runOnce(t, &toolexec.ToolResult{
		IsError:   true,
		ErrorCode: "EXECUTION_ERROR",
		Content:   "command blocked by the scan guard",
	})

	require.NoError(t, err)
	require.Equal(t, 1, exitCode)
	require.Contains(t, stderr, "scan guard")
}

// And a command that genuinely ran and exited non-zero keeps its own exit code.
func TestRealNonZeroExitIsPreserved(t *testing.T) {
	stdout, stderr, exitCode, err := runOnce(t, &toolexec.ToolResult{
		Success: true,
		Content: `{"stdout":"FAIL ./internal/handlers","stderr":"1 test failed","exit_code":2}`,
	})

	require.NoError(t, err)
	require.Equal(t, 2, exitCode)
	require.Equal(t, "FAIL ./internal/handlers", stdout)
	require.Equal(t, "1 test failed", stderr)
}
