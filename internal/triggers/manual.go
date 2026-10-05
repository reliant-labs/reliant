// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// StartManualFire runs a trigger now, bypassing its enabled flag and overlap
// policy. It returns the fire workflow id, which is also the dedupe key of the
// event the fire will record.
//
// This starts the fire workflow directly rather than calling
// ScheduleHandle.Trigger. Trigger would be subject to the schedule's own
// policies and would produce a fire the server labels as scheduled, so a
// "run now" on a paused schedule — the case people actually want, because they
// are testing a trigger before enabling it — would be declined.
func StartManualFire(ctx context.Context, starter WorkflowStarter, triggerID, taskQueue string) (string, error) {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	id := ManualFireWorkflowID(triggerID)
	_, err := starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                taskQueue,
		WorkflowExecutionTimeout: fireWorkflowTimeout,
		// One attempt at the workflow level. The activity has its own retry
		// policy, which is where a transient failure should be absorbed;
		// restarting the workflow would change nothing and would duplicate the
		// backoff.
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	}, FireWorkflowName, FireInput{TriggerID: triggerID, Manual: true})
	if err != nil {
		return "", fmt.Errorf("start manual fire for trigger %s: %w", triggerID, err)
	}
	return id, nil
}

// SetMinIntervalForTest lowers the interval floor so tests can use a schedule
// that fires within a bounded wait. It returns a restore func.
func SetMinIntervalForTest(d time.Duration) func() {
	return triggerspec.SetMinIntervalForTest(d)
}
