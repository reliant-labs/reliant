// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// levelRecord is one line a levelLogger received.
type levelRecord struct {
	level, msg string
	keyvals    []interface{}
}

// levelLogger records the level of every line logged through it.
type levelLogger struct{ records []levelRecord }

func (l *levelLogger) add(level, msg string, keyvals []interface{}) {
	l.records = append(l.records, levelRecord{level, msg, keyvals})
}
func (l *levelLogger) Debug(msg string, keyvals ...interface{}) { l.add("DEBUG", msg, keyvals) }
func (l *levelLogger) Info(msg string, keyvals ...interface{})  { l.add("INFO", msg, keyvals) }
func (l *levelLogger) Warn(msg string, keyvals ...interface{})  { l.add("WARN", msg, keyvals) }
func (l *levelLogger) Error(msg string, keyvals ...interface{}) { l.add("ERROR", msg, keyvals) }

// Every ERROR opens an obs issue, so logFailure keeps it for faults: a user's
// cancel, passed up through the loop, the inline workflow and the run, is an
// outcome and logs at INFO with its fields (reliant #2395, prod 4d68df91).
func TestLogFailure_Levels(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"a fault":                          {errors.New("activity call_llm failed: provider returned 500"), "ERROR"},
		"the user's cancel":                {ErrThreadCancelled, "INFO"},
		"the user's cancel, wrapped":       {fmt.Errorf("inline workflow agent failed: %w", ErrThreadCancelled), "INFO"},
		"a failure already reported":       {markReported(errors.New("tool calls keep failing")), "DEBUG"},
		"another error with the same text": {errors.New("thread cancelled by user"), "ERROR"},
	} {
		logger := &levelLogger{}
		logFailure(logger, tc.err, "[InlineLoop] Iteration failed, exiting loop", "loopID", "agent", "error", tc.err)
		require.Len(t, logger.records, 1, name)
		got := logger.records[0]
		assert.Equal(t, tc.want, got.level, name)
		assert.Equal(t, "[InlineLoop] Iteration failed, exiting loop", got.msg, name)
		assert.Equal(t, []interface{}{"loopID", "agent", "error", tc.err}, got.keyvals, name)
	}
}

// The identical-tool-failure guard ending an agent is the guard working, so
// it is reported once at WARN with its fields, and marked so the layers that
// pass it up log it at DEBUG rather than ERROR (prod bc65dd1b).
func TestReportExpectedStop_WarnsOnceAndMarks(t *testing.T) {
	t.Parallel()
	logger := &levelLogger{}
	cause := errors.New("github__repo_get_tree: GitHub returned not found")

	err := reportExpectedStop(logger, cause, "[StepExecutor] Ending the agent: its tool calls keep failing identically",
		"stepID", "s1", "thread", "t1", "error", cause)

	require.Len(t, logger.records, 1)
	assert.Equal(t, "WARN", logger.records[0].level)
	assert.Equal(t, []interface{}{"stepID", "s1", "thread", "t1", "error", cause}, logger.records[0].keyvals)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, cause.Error(), err.Error())

	logFailure(logger, fmt.Errorf("loop agent failed: %w", err), "[Workflow Runtime] Failed with error")
	require.Len(t, logger.records, 2)
	assert.Equal(t, "DEBUG", logger.records[1].level, "an outer layer must not report it again")
}

// activityFailure returns the error a workflow really observes when an activity
// fails with cause, from the Temporal test environment (the SDK does not
// export NewActivityError). One attempt: the retry ladder is not under test.
func activityFailure(t *testing.T, cause error) error {
	t.Helper()
	var ts temporaltest.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context) error { return cause }, activity.RegisterOptions{Name: "Fails"})
	var got error
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
		})
		got = workflow.ExecuteActivity(actCtx, "Fails").Get(ctx, nil)
		return nil
	})
	require.True(t, env.IsWorkflowCompleted())
	var activityErr *temporal.ActivityError
	require.True(t, errors.As(got, &activityErr), "want *ActivityError, got %T", got)
	return got
}

// A terminal activity failure is logged once, by the ActivityWrapper, where it
// happened. The workflow layers it passes through (InlineLoop, SpawnInline,
// the run) log it at DEBUG: one failed call_llm was three ERROR issues on
// 2026-11-17 (prod d25b380e, cc984602, 965eb91e). A retryable failure whose
// retries ran out was only ever logged at WARN, so it stays an ERROR here.
func TestLogFailure_TerminalActivityFailureIsAlreadyReported(t *testing.T) {
	t.Parallel()
	terminal := activityFailure(t, temporal.NewNonRetryableApplicationError("provider rejected the request", "TerminalError", nil))
	exhausted := activityFailure(t, temporal.NewApplicationError("429 Too Many Requests", "RateLimitError"))

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"a terminal activity failure":                     {terminal, "DEBUG"},
		"a terminal activity failure, wrapped by a layer": {fmt.Errorf("inline workflow agent failed: %w", terminal), "DEBUG"},
		"a retryable activity failure out of retries":     {exhausted, "ERROR"},
	} {
		logger := &levelLogger{}
		logFailure(logger, tc.err, "[SpawnInline] Inline execution failed", "error", tc.err)
		require.Len(t, logger.records, 1, name)
		assert.Equal(t, tc.want, logger.records[0].level, name)
	}
}
