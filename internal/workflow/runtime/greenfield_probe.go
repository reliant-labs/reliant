// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// GreenfieldProbeActivityName is the activity that asks the run's daemon
// whether the working directory holds code and, when it does not, seeds the
// greenfield stack guidance into the thread. See handlers.GreenfieldProbeActivity.
const GreenfieldProbeActivityName = "GreenfieldProbe"

// greenfieldProbeBudget is the most the probe may add in front of the first
// LLM call, queueing included (schedule-to-close, not start-to-close: a
// backlogged task queue must not stretch it either).
//
// The activity bounds its own daemon round trip tighter than this (see
// handlers.greenfieldProbeTimeout) so the common failure — a daemon that is
// connected but slow — comes back as an ordinary "probe_failed" outcome, and
// this budget only fires on something stranger, like a hung database. One
// attempt: the guidance is a nicety, and a retry would spend the budget twice.
const greenfieldProbeBudget = 3 * time.Second

// runGreenfieldProbe runs the greenfield probe and waits for it, best-effort.
//
// The probe used to run on the request path (StartChat, SendMessage), which
// made a chat's creation wait on a synchronous round trip to a daemon that may
// be remote, suspended or cold. It belongs to the run: the run already owns
// the daemon conversation, it runs after preflight has woken the machine, and
// the only thing that needs the answer is the first LLM call.
//
// Replay: whether this schedules an activity is decided by
// WorkflowInput.GreenfieldProbe, which is fixed in the recorded start event. A
// history recorded before the field existed decodes it as false, so its
// command sequence is unchanged — which is why there is no version gate here.
// Every failure (an activity error, a timeout) is logged and swallowed: no
// outcome of this step may change what the run does next.
func runGreenfieldProbe(ctx workflow.Context, input WorkflowInput, execCtx *ExecutionContext, workflowID, thread string) {
	probeCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: greenfieldProbeBudget,
		RetryPolicy:            &temporal.RetryPolicy{MaximumAttempts: 1},
	})

	probeInput := map[string]interface{}{
		"chat_id":     input.ChatID,
		"workflow_id": workflowID,
		"thread":      thread,
	}
	if projectPath, ok := input.Inputs["project_path"].(string); ok {
		probeInput["project_path"] = projectPath
	}
	// Probe the machine this run's tools execute on. A selector by name or
	// type cannot be resolved here, so it falls back to the user's default
	// daemon, which is also where such a selector most often lands.
	if execCtx != nil && execCtx.DaemonSelector != nil && execCtx.DaemonSelector.ID != "" {
		probeInput["daemon_id"] = execCtx.DaemonSelector.ID
	}

	var result map[string]interface{}
	if err := workflow.ExecuteActivity(probeCtx, GreenfieldProbeActivityName, probeInput).Get(ctx, &result); err != nil {
		workflow.GetLogger(ctx).Warn("[Workflow Runtime] Greenfield probe did not finish; continuing without it",
			"chatID", input.ChatID,
			"error", err,
		)
		return
	}
	workflow.GetLogger(ctx).Debug("[Workflow Runtime] Greenfield probe finished",
		"chatID", input.ChatID,
		"outcome", result["outcome"],
	)
}
