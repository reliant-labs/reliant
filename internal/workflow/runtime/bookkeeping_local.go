// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// bookkeepingLocalGate is the GetVersion changeID that moves the run's short
// bookkeeping activities — loading a workflow definition
// (ActivityLoadWorkflow), loading a preset (LoadPresetParams) and writing the
// position checkpoint (WorkflowCheckpoint) — from regular activities to local
// activities.
//
// # Why
//
// A regular activity costs Temporal's database four UpdateWorkflowExecution
// writes (activity started, activity completed, and the workflow task its
// completion schedules: started, completed), a matching task, and six history
// events. A local activity runs inside the workflow task that asked for it
// and records ONE marker event in that task's completion: no extra write.
// Measured in prod on 2026-10-30, these three were 0.40 of the fleet's 3.3
// activities/s (about 1.6 of 25.5 UpdateWorkflowExecution/s), and 42 of 196
// activities in an orchestrator chat's 86-second window, the definitions
// alone 750KB of it.
//
// They qualify because each is short (0.3-0.9s mean), idempotent (two reads
// and an upsert), and needed before the workflow can do anything else, so
// nothing is gained by letting the workflow task end while they run.
//
// # Why one changeID for every call site
//
// GetVersion records its marker once per run per changeID, and caches the
// answer for the rest of the run. A run that started before this shipped
// replays its first call site with no marker in history, so every site stays
// a regular activity for that whole run, matching its history; a run that
// starts after records version 1 at its first site (the definition load in
// its first workflow task) and every site is local. One marker per run, and
// no site can disagree with another about which era the run is in.
//
// Gating it is required, not cautious: replay matches a recorded
// ActivityTaskScheduled event against the command the code emits, and a
// local activity emits a marker instead (TMPRL1100 on every in-flight run
// without the gate). Pinned by replaytest's frozen fixtures, which carry the
// regular-activity shape.
const bookkeepingLocalGate = "bookkeeping-local-activities"

// executeBookkeeping runs one bookkeeping activity under opts: as a local
// activity once the run is past bookkeepingLocalGate, as a regular activity
// before it. The timeouts and retry policy carry over unchanged; a local
// activity's ScheduleToClose bounds its retries the way the attempts would.
func executeBookkeeping(ctx workflow.Context, opts workflow.ActivityOptions, activityName string, input interface{}) workflow.Future {
	if workflow.GetVersion(ctx, bookkeepingLocalGate, workflow.DefaultVersion, 1) < 1 {
		return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), activityName, input)
	}
	return workflow.ExecuteLocalActivity(workflow.WithLocalActivityOptions(ctx, localOptionsFor(opts)), activityName, input)
}

// localOptionsFor maps a regular activity's options onto a local activity's.
func localOptionsFor(opts workflow.ActivityOptions) workflow.LocalActivityOptions {
	local := workflow.LocalActivityOptions{
		StartToCloseTimeout: opts.StartToCloseTimeout,
		RetryPolicy:         opts.RetryPolicy,
	}
	if opts.RetryPolicy != nil && opts.RetryPolicy.MaximumAttempts > 0 {
		// Every attempt at its full timeout plus the largest backoff between
		// them: the same bound the attempt count implied.
		attempts := opts.RetryPolicy.MaximumAttempts
		local.ScheduleToCloseTimeout = opts.StartToCloseTimeout*time.Duration(attempts) +
			opts.RetryPolicy.MaximumInterval*time.Duration(attempts-1)
	}
	return local
}
