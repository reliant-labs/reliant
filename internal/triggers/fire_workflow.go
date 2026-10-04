// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// Names registered with Temporal. They are strings rather than function
// references everywhere outside workersetup so that packages wiring a
// schedule, or starting a manual fire, need not import the worker.
const (
	FireWorkflowName = "TriggerFireWorkflow"
	FireActivityName = "FireTrigger"
)

// defaultTaskQueue is the queue the fire workflow runs on when none is given.
const defaultTaskQueue = v2workflow.SharedTaskQueue

// fireWorkflowTimeout bounds one fire. The work is a few DB reads plus one
// launch; anything slower is a stuck system, not a slow schedule.
const fireWorkflowTimeout = 10 * time.Minute

// nonRetryableFireError is the error type the activity uses for a fire that
// can never succeed as written — a trigger whose workflow does not exist, or
// whose model is unavailable. It is listed as non-retryable below so the
// failure is recorded once and the fire ends, instead of being re-attempted
// every backoff interval until the workflow times out.
const nonRetryableFireError = "TriggerFireValidation"

// FireInput is the fire workflow's and activity's input.
type FireInput struct {
	TriggerID string `json:"trigger_id"`
	// Manual marks a "run now": it bypasses the enabled and overlap checks,
	// because the person who pressed the button is the authority on both.
	Manual bool `json:"manual,omitempty"`
}

// FireRequest is what the workflow hands the activity: the input plus the two
// facts only the workflow can read.
//
// The scheduled time and the fire workflow id live in the WORKFLOW's identity
// and search attributes, and an activity has access to neither — activity.Info
// exposes the workflow's id but nothing the server attached to it. So the
// workflow resolves both and passes them down, which also makes them
// substitutable in the activity's unit tests.
type FireRequest struct {
	TriggerID string `json:"trigger_id"`
	Manual    bool   `json:"manual,omitempty"`
	// FireWorkflowID is this fire's workflow id. It is the event row's dedupe
	// key and the seed of the chat id, so it is the whole of the fire's
	// idempotency.
	FireWorkflowID string `json:"fire_workflow_id"`
	// ScheduledAt is the time the schedule intended to fire, which is not the
	// time the fire ran: a catchup after an outage, or a retried workflow
	// task, moves the latter and must not move the former.
	ScheduledAt time.Time `json:"scheduled_at"`
}

// FireOutput reports what the fire did, for the Temporal UI and tests.
type FireOutput struct {
	Outcome string `json:"outcome"` // launched | skipped | failed
	Reason  string `json:"reason,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	EventID string `json:"event_id,omitempty"`
}

// scheduledStartTimeAttr is the search attribute the Temporal server attaches
// to a workflow it started from a schedule. It is a server-maintained
// attribute, pre-registered in every namespace, and holds the time the action
// was scheduled for including jitter.
//
// Confirmed populated by the dev server shipped with temporal CLI 1.4.1
// (Server 1.28.0): TestScheduleFiresThroughToTheLauncher asserts the value
// equals the timestamp Temporal appends to the fire's workflow id, and fails
// by ~180ms when the read below is removed — so the fallback is genuinely a
// fallback, not what the schedule path actually uses.
//
// The fallback still has to exist, for the case the attribute is absent: a
// manual "run now" starts the workflow directly and the server attaches
// nothing, and WorkflowStartTime is the right answer there anyway.
const scheduledStartTimeAttr = "TemporalScheduledStartTime"

// TriggerFireWorkflow is one firing of a schedule. It exists only to give the
// fire a durable identity: its workflow id is unique per scheduled time, and
// that id is the event row's dedupe key, which is what makes a retried fire
// idempotent rather than a second run.
func TriggerFireWorkflow(ctx workflow.Context, input FireInput) (*FireOutput, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
			// Validation failures are recorded as a failed event by the
			// activity itself, so retrying them would only re-record the same
			// verdict. Everything else — a database blip, Temporal being
			// briefly unreachable — is worth retrying.
			NonRetryableErrorTypes: []string{nonRetryableFireError},
		},
	})

	req := FireRequest{
		TriggerID:      input.TriggerID,
		Manual:         input.Manual,
		FireWorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID,
		ScheduledAt:    scheduledTime(ctx),
	}

	var out FireOutput
	if err := workflow.ExecuteActivity(ctx, FireActivityName, req).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// scheduledTime is the time this fire was scheduled for.
//
// Prefer the server's TemporalScheduledStartTime, which is the scheduled time
// including jitter and is stable across workflow task retries. Fall back to
// the workflow start time, which is what a manual fire has and is also correct
// for it: a "run now" is scheduled for the moment it was asked for.
//
// Both reads are deterministic (search attributes and the start time are
// replayed from history), so this is safe in workflow code.
func scheduledTime(ctx workflow.Context) time.Time {
	key := temporal.NewSearchAttributeKeyTime(scheduledStartTimeAttr)
	if t, ok := workflow.GetTypedSearchAttributes(ctx).GetTime(key); ok && !t.IsZero() {
		return t
	}
	return workflow.GetInfo(ctx).WorkflowStartTime
}
