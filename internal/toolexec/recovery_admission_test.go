// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/observability"
)

// A daemon refusing a command while it finalizes a worktree's recovery is
// admission control working: the router's error says so (errors.Is, a user
// error, a retryable refusal) and the refusal is counted. A genuine daemon
// failure is none of these, and is not counted (reliant#2429).
func TestRecoveryRefusal_DaemonCommandIsCountedAndNotAServerFault(t *testing.T) {
	refused := observability.DaemonRecoveryAdmissionRefusedTotal.WithLabelValues(recoveryRefusedCommand)

	for name, tc := range map[string]struct {
		text    string
		refusal bool
	}{
		"recovery finalizing": {text: ErrRecoveryFinalizing.Error(), refusal: true},
		"genuine failure":     {text: "git status: exit status 128: fatal: not a git repository"},
	} {
		t.Run(name, func(t *testing.T) {
			_, router := startBridgeAndRouter(t, &payloadTestMgr{
				cmdResp: &reliantv1.DaemonCommandResponse{Success: false, ErrorMessage: tc.text},
			})
			before := testutil.ToFloat64(refused)

			_, err := router.SendDaemonCommand(context.Background(), "user-1", "exec.bg_list", []byte(`{}`), 5000)
			require.Error(t, err)

			text, ok := DaemonReportedText(err)
			require.True(t, ok, "still the daemon's own report")
			assert.Equal(t, tc.text, text, "the daemon's words are unchanged")
			assert.Equal(t, tc.refusal, IsRecoveryFinalizing(err))
			if tc.refusal {
				assert.Equal(t, before+1, testutil.ToFloat64(refused))
				assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
				assert.Equal(t, svcerr.ClassUser, errclass.Library(svcerr.WithCause(svcerr.Internal("daemon command exec.bg_list failed"), err)),
					"seen behind a handler's own WithCause too")
			} else {
				assert.Equal(t, before, testutil.ToFloat64(refused))
				assert.Equal(t, svcerr.ClassServer, svcerr.Classify(err))
				assert.Equal(t, svcerr.ClassNone, errclass.Library(svcerr.WithCause(svcerr.Internal("daemon command exec.bg_list failed"), err)))
			}
		})
	}
}

// A tool call the daemon refused for the same reason is counted under its own
// kind; any other tool error is not.
func TestRecoveryRefusal_ToolCallIsCounted(t *testing.T) {
	refused := observability.DaemonRecoveryAdmissionRefusedTotal.WithLabelValues(recoveryRefusedToolCall)

	for name, tc := range map[string]struct {
		code  string
		delta float64
	}{
		"recovery finalizing": {code: ErrorCodeRecoveryFinalizing, delta: 1},
		"tool's own failure":  {code: "EXECUTION_ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			_, router := startBridgeAndRouter(t, &payloadTestMgr{toolResp: &ToolExecutionResponse{
				RequestID: "req-1", Success: false, IsError: true,
				ErrorMessage: ErrRecoveryFinalizing.Error(), ErrorCode: tc.code,
			}})
			before := testutil.ToFloat64(refused)

			resp, err := router.SendToolRequestSync(context.Background(), "user-1",
				&ToolExecutionRequest{RequestID: "req-1", ToolName: "shell", ToolInput: `{}`, TimeoutMs: 5000})
			require.NoError(t, err)
			assert.Equal(t, tc.code, resp.ErrorCode)
			assert.Equal(t, before+tc.delta, testutil.ToFloat64(refused))
		})
	}
}
