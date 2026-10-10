// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
)

// incidentStallChain is CallLLM's error from chat 622675c2 exactly as it was
// built: four %w layers around the transport's stall sentinel.
func incidentStallChain() error {
	stall := errors.New("llm stream content stall timeout: provider sent only keepalives before the content deadline")
	return fmt.Errorf("failed to stream LLM response: %w",
		fmt.Errorf("%s: %w",
			"the anthropic provider accepted the request but sent no content for 5m0s; retrying. If this repeats, check that the subscription has remaining credit [RELIANT_PROVIDER_STREAM_STALLED:anthropic]",
			fmt.Errorf("LLM streaming error: %w", stall)))
}

// runWrappedFailure runs fn through the production activity boundary
// (wrapActivity) and returns the error the WORKFLOW receives — after the
// failure converter has serialized it, which is where the duplication was born.
func runWrappedFailure(t *testing.T, fn func(context.Context, string) (string, error)) error {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	registry := NewActivityRegistry(&wrapperTestRepo{})
	env.RegisterActivityWithOptions(wrapActivity(registry, "CallLLM", fn, lifecycle.AgentWork, false),
		activity.RegisterOptions{Name: "CallLLM"})
	_, err := env.ExecuteActivity("CallLLM", "input")
	require.Error(t, err)
	return err
}

// The paused chat showed the stall explanation four times over:
//
//	activity error (...): failed to stream LLM response: X [M]: LLM streaming
//	error: Y (type: wrapError, retryable: true): X [M]: LLM streaming error: Y
//	(type: wrapError, retryable: true): LLM streaming error: Y (type: wrapError,
//	retryable: true): Y
//
// Temporal's failure converter records one failure PER Go wrap layer, each
// carrying that layer's full text, and ApplicationError.Error() then prints
// every layer followed by its cause. The causal chain belongs in the failure
// once.
func TestWrappedActivity_ReportsTheCausalChainOnce(t *testing.T) {
	t.Parallel()
	err := runWrappedFailure(t, func(context.Context, string) (string, error) {
		return "", incidentStallChain()
	})

	msg := err.Error()
	assert.Equal(t, 1, strings.Count(msg, "accepted the request"), "stall explanation repeated: %s", msg)
	assert.Equal(t, 1, strings.Count(msg, "LLM streaming error"), "wrap prefix repeated: %s", msg)
	assert.Equal(t, 1, strings.Count(msg, "keepalives before the content deadline"), "root cause repeated: %s", msg)
	assert.Contains(t, msg, "[RELIANT_PROVIDER_STREAM_STALLED:anthropic]", "the chat marker must survive for the UI")

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "the workflow must still see an ApplicationError")
	assert.False(t, appErr.NonRetryable(), "flattening must not change retryability")
}

// selfScheduledFailure stands in for CallLLM's stalled-stream error, which
// lives (unexported) in the handlers package.
type selfScheduledFailure struct {
	delay time.Duration
	retry bool
}

func (e selfScheduledFailure) Error() string {
	return "AI provider stopped responding: claude-5.5-sonnet on anthropic accepted the request but sent no output for 5m0s (usually an exhausted subscription or an overloaded provider) [RELIANT_PROVIDER_STREAM_STALLED:anthropic]: llm stream content stall timeout: provider sent only keepalives for 5m0s with no content block open"
}
func (e selfScheduledFailure) RetryAfter() (time.Duration, bool) { return e.delay, e.retry }
func (e selfScheduledFailure) TemporalErrorType() string         { return "ProviderStreamStalled" }

// A stalled stream schedules its own retry: each attempt has already cost a
// whole stall deadline, so the step's 5-attempts-a-second-apart policy does
// not fit it. The schedule must reach the workflow intact through the wrap
// CallLLM puts around it — before flattening, an ApplicationError under a
// plain fmt.Errorf left the top failure retryable whatever it said.
func TestWrappedActivity_StallSchedulesItsOwnRetry(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		failure      selfScheduledFailure
		nonRetryable bool
	}{
		"retry after a backoff": {failure: selfScheduledFailure{delay: 30 * time.Second, retry: true}},
		"decline the retry":     {failure: selfScheduledFailure{retry: false}, nonRetryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := runWrappedFailure(t, func(context.Context, string) (string, error) {
				return "", fmt.Errorf("failed to stream LLM response: %w", tc.failure)
			})

			var appErr *temporal.ApplicationError
			require.True(t, errors.As(err, &appErr))
			assert.Equal(t, "ProviderStreamStalled", appErr.Type())
			assert.Equal(t, tc.nonRetryable, appErr.NonRetryable())
			assert.Equal(t, tc.failure.delay, appErr.NextRetryDelay())
			assert.Equal(t, 1, strings.Count(err.Error(), "AI provider stopped responding"), err.Error())
			assert.Equal(t, tc.nonRetryable, isTerminal(tc.failure),
				"the chat row must stop saying \"retrying\" on the attempt that pauses")
		})
	}
}

// Shapes the SDK or the workflow keys on by identity must not be rebuilt.
func TestFlattenForTemporal_LeavesCancellationAndDetailsAlone(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"context.Canceled under wraps": fmt.Errorf("stream: %w", fmt.Errorf("read: %w", context.Canceled)),
		"CanceledError":                temporal.NewCanceledError("paused"),
		"ApplicationError with details": temporal.NewApplicationErrorWithOptions("bad", "Custom",
			temporal.ApplicationErrorOptions{Details: []interface{}{"detail"}, Cause: errors.New("inner")}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Same(t, err, flattenForTemporal(err))
		})
	}
	assert.Nil(t, flattenForTemporal(nil))
}

// An ApplicationError whose message does not repeat its cause keeps both,
// once each: the wrapper's HeartbeatCancel names what happened, its cause
// says why.
func TestFlattenForTemporal_KeepsACauseTheMessageDoesNotRepeat(t *testing.T) {
	t.Parallel()
	err := temporal.NewApplicationErrorWithCause("heartbeat RPC failed while running CallLLM; retrying", "HeartbeatCancel",
		fmt.Errorf("streaming cancelled by user: %w", context.DeadlineExceeded))

	flat := flattenForTemporal(err)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(flat, &appErr))
	assert.Equal(t, "HeartbeatCancel", appErr.Type(), "heartbeatCancelExhausted keys on this type")
	assert.Equal(t, "heartbeat RPC failed while running CallLLM; retrying: streaming cancelled by user: context deadline exceeded",
		appErr.Message())
	assert.Nil(t, errors.Unwrap(flat), "the chain is in the message, not in nested failures")
}

// A terminal error was wrapped as NonRetryableApplicationError(err.Error(),
// "TerminalError", err) — its message AND its cause are the same text, so it
// rendered twice even with a single Go layer.
func TestWrappedActivity_TerminalErrorRendersOnce(t *testing.T) {
	t.Parallel()
	err := runWrappedFailure(t, func(context.Context, string) (string, error) {
		return "", fmt.Errorf("resolve worktree: %w", errors.New("worktree wt-123 not found"))
	})

	msg := err.Error()
	assert.Equal(t, 1, strings.Count(msg, "wt-123 not found"), "terminal cause repeated: %s", msg)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr))
	assert.True(t, appErr.NonRetryable(), "a terminal error stays terminal")
	assert.Equal(t, "TerminalError", appErr.Type(), "the workflow keys on this type")
}
