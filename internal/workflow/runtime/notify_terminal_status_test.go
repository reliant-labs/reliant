// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

func notifyStatusWorkflow(ctx workflow.Context, status string) error {
	notifyWorkflowStatus(ctx, "chat-1", "wf-1", "builtin://agent", status, "", "thread-1", nil)
	return nil
}

// statusWrites stands in for the WorkflowStatus activity over a database that
// refuses the first failures writes, as one too loaded to commit them would.
type statusWrites struct {
	failures int

	mu       sync.Mutex
	attempts int
	landed   []string
}

func (s *statusWrites) activity(ctx context.Context, in map[string]interface{}) (map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if int(activity.GetInfo(ctx).Attempt) <= s.failures {
		return nil, errors.New("injected: status write did not commit")
	}
	s.landed = append(s.landed, in["status"].(string))
	return map[string]interface{}{"success": true}, nil
}

// A run's terminal status is its last write to its row, so it is retried
// until it lands: six refused writes — twice the three attempts it used to
// get before being dropped, which left the row ACTIVE over a finished run
// (e2e story 05) — still end with it written.
func TestNotifyWorkflowStatus_TerminalStatusIsRetriedUntilItLands(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			writes := &statusWrites{failures: 6}
			env.RegisterActivityWithOptions(writes.activity, activity.RegisterOptions{Name: "WorkflowStatus"})

			env.ExecuteWorkflow(notifyStatusWorkflow, status)

			require.NoError(t, env.GetWorkflowError())
			assert.Equal(t, []string{status}, writes.landed, "the terminal status was written")
			assert.Equal(t, 7, writes.attempts, "six refused attempts, then the one that landed")
		})
	}
}

// An in-flight status keeps the short budget: the run's next status
// supersedes it, so the run does not wait on it.
func TestNotifyWorkflowStatus_InFlightStatusKeepsItsShortBudget(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	writes := &statusWrites{failures: 6}
	env.RegisterActivityWithOptions(writes.activity, activity.RegisterOptions{Name: "WorkflowStatus"})

	env.ExecuteWorkflow(notifyStatusWorkflow, "started")

	require.NoError(t, env.GetWorkflowError(), "a lost in-flight status never fails the run")
	assert.Empty(t, writes.landed)
	assert.Equal(t, 3, writes.attempts)
}

func TestWorkflowStatusActivityOptions_TerminalStatusIsBounded(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "cancelled"} {
		opts := workflowStatusActivityOptions(status)
		assert.Equal(t, terminalStatusAttemptTimeout, opts.StartToCloseTimeout, status)
		assert.Equal(t, terminalStatusDeadline, opts.ScheduleToCloseTimeout, "%s: every attempt together is bounded", status)
		assert.Zero(t, opts.RetryPolicy.MaximumAttempts, "%s: attempts are bounded by the deadline, not a count", status)
	}
}
