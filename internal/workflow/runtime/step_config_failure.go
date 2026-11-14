// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// stepConfigFailedChangeID gates ending the run when a step's own config
// cannot run (FailStep). Before it, such a step paused the run like a
// transient failure: an unattended run then self-resumed into the same
// failure on every rung of its ladder, never ran its on_failure handler, and
// held its automation's capacity slot while it sat paused (control-plane
// pr-review, 2026-10-31: six runs over a CEL cost limit took all six slots).
// Older histories keep pausing on replay.
const stepConfigFailedChangeID = "step-config-failure-ends-run"

// stepConfigFailedErrType is the error a run ends with when one of its steps'
// config cannot run: a {{ }} expression that does not evaluate (a missing
// key, a CEL cost limit), or a node the runtime cannot dispatch. It is
// decided by the definition and the data it reads, so no resume or retry
// can change it: the run fails, its on_failure handler runs, and an
// automation's capacity slot is freed.
const stepConfigFailedErrType = "StepConfigFailed"

// failStepActivityName is the activity StepExecutor dispatches for a step
// whose config cannot run (startFailedStep, executeFailActivity).
const failStepActivityName = "FailStep"

// stepConfigFailure is the error a FailStep completion ends the run with, or
// nil to keep the old behaviour (pause) on a history recorded before
// stepConfigFailedChangeID.
func stepConfigFailure(ctx workflow.Context, activityName, stepID string, err error) error {
	if activityName != failStepActivityName || err == nil {
		return nil
	}
	if workflow.GetVersion(ctx, stepConfigFailedChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	// The activity's message ("step cannot run as configured: CEL evaluation
	// failed for step b: …"), without Temporal's "activity error (type: …)"
	// envelope around it.
	msg := err.Error()
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		msg = appErr.Message()
	}
	// Benign: the definition is the user's, so the layers that pass this up
	// (the loop, the inline workflow, the run) log it below ERROR through the
	// process logger's error-class policy (errclass.Library). Each used to
	// open an obs issue for a workflow that was simply misconfigured.
	return temporal.NewApplicationErrorWithOptions(fmt.Sprintf("step %s failed: %s", stepID, msg), stepConfigFailedErrType,
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
}
