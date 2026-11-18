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

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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

// wrappedActivityFailure returns the error a workflow really observes when an
// activity run through the ActivityWrapper and wrapActivity's outbound path
// fails with cause, in the Temporal test environment (the SDK does not export
// NewActivityError). name picks the wrapper's verdict: FailStep's terminal
// failures are reported on the run, at WARN.
func wrappedActivityFailure(t *testing.T, name string, cause error) error {
	t.Helper()
	wrapper := NewActivityWrapper(name,
		func(context.Context, types.DrainAgentMessagesInput) (types.DrainAgentMessagesOutput, error) {
			return types.DrainAgentMessagesOutput{}, cause
		}, nil)
	var ts temporaltest.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, in types.DrainAgentMessagesInput) (types.DrainAgentMessagesOutput, error) {
		out, err := wrapper.Execute(ctx, in)
		return out, flattenForTemporal(classifyError(err))
	}, activity.RegisterOptions{Name: name})
	var got error
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
		})
		got = workflow.ExecuteActivity(actCtx, name, types.DrainAgentMessagesInput{}).Get(ctx, nil)
		return nil
	})
	require.True(t, env.IsWorkflowCompleted())
	var activityErr *temporal.ActivityError
	require.True(t, errors.As(got, &activityErr), "want *ActivityError, got %T", got)
	return got
}

// A failure the ActivityWrapper logged at ERROR is that failure's one report:
// the workflow layers it passes through (InlineLoop, SpawnInline, the run) log
// it at DEBUG. One failed activity in a sub-agent was three ERROR issues on
// 2026-11-17 (prod d25b380e, cc984602, 965eb91e). A failure the wrapper logged
// below ERROR carries no marker, so the layer returning it still reports it:
// a verdict the run reports (WARN), a user's error (lowered to INFO), a
// deadline (WARN), and a retryable failure out of retries (WARN per attempt).
func TestLogFailure_OnlyAnActivityFailureLoggedAtErrorIsAlreadyReported(t *testing.T) {
	t.Parallel()
	terminal := func(msg string) error { return temporal.NewNonRetryableApplicationError(msg, "TerminalError", nil) }
	serverFault := wrappedActivityFailure(t, "DrainAgentMessages", terminal("the provider rejected the request"))

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"a terminal server fault the wrapper logged at ERROR": {serverFault, "DEBUG"},
		"the same, wrapped by a workflow layer":               {fmt.Errorf("inline workflow agent failed: %w", serverFault), "DEBUG"},
		"a terminal verdict the run reports (wrapper: WARN)":  {wrappedActivityFailure(t, failStepActivityName, terminal("missing key in {{ }}")), "ERROR"},
		"a terminal user error (wrapper: lowered to INFO)":    {wrappedActivityFailure(t, "DrainAgentMessages", svcerr.WithClass(&TerminalError{Message: "your key was rejected"}, svcerr.ClassUser)), "ERROR"},
		"a terminal deadline (wrapper: WARN)":                 {wrappedActivityFailure(t, "DrainAgentMessages", &TerminalError{Message: "machine timed out", Cause: svcerr.DeadlineExceeded("the machine did not answer in time")}), "ERROR"},
		"a retryable failure out of retries (wrapper: WARN)":  {wrappedActivityFailure(t, "DrainAgentMessages", temporal.NewApplicationError("429 Too Many Requests", "RateLimitError")), "ERROR"},
	} {
		logger := &levelLogger{}
		logFailure(logger, tc.err, "[SpawnInline] Inline execution failed", "error", tc.err)
		require.Len(t, logger.records, 1, name)
		assert.Equal(t, tc.want, logger.records[0].level, name)
	}
}
