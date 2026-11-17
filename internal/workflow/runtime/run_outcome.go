// Copyright (c) 2025 Reliant Labs
package runtime

import "go.temporal.io/sdk/workflow"

// A run's outcome (core.Workflow.RunOutcome, reliant#2753) is judged by the
// WorkflowStatus activity when the run ends: a COMPLETED run whose node
// attempts since it began include one whose last attempt failed went on past
// a failure, and is FAILURE_HANDLED rather than SUCCEEDED. The workflow's part
// is only to say when the run began, which a continue-as-new successor must
// carry: its own execution started later than the run it continues.

// runStartedAtMs is when the run began, in unix ms: the start of the run a
// continue-as-new successor continues, else this execution's. Read from
// workflow info and the input, so a replay computes the same value.
func runStartedAtMs(ctx workflow.Context, resume *ResumeInput) int64 {
	if resume != nil && resume.RunStartedAtMs > 0 {
		return resume.RunStartedAtMs
	}
	return workflow.GetInfo(ctx).WorkflowStartTime.UnixMilli()
}

// startedAtMs is when the tracker's run began, 0 when unknown (a bare tracker
// in tests): the status activity then judges every attempt the run has.
func (t *ChildWorkflowTracker) startedAtMs() int64 {
	if t == nil {
		return 0
	}
	return t.runStartedAtMs
}
