// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// StatusLabel renders a lifecycle pair as the lowercase word used everywhere a
// run's status is spoken of: "pending", "running", "completed", "failed",
// "paused", "cancelled". A STOPPED run with no recognised reason is "stopped",
// and an unset state is "unknown".
//
// It is the LIFECYCLE only. Whether the work passed is the run's outcome; use
// [Succeeded] to combine them.
func StatusLabel(state reliantv1.WorkflowState, reason reliantv1.WorkflowStopReason) string {
	switch state {
	case reliantv1.WorkflowState_WORKFLOW_STATE_PENDING:
		return "pending"
	case reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE:
		return "running"
	case reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED:
		switch reason {
		case reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED:
			return "completed"
		case reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED:
			return "failed"
		case reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED:
			return "paused"
		case reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED:
			return "cancelled"
		}
		return "stopped"
	}
	return "unknown"
}

// Succeeded is the one place a run's lifecycle and verdict are combined: the
// run finished cleanly AND did not declare itself a failure.
//
// A run that routes to its `failed` terminal node completes normally, so the
// lifecycle alone reports it as a success. An empty outcome means the workflow
// declared none, which is not a failure. Every caller answering "did this run
// succeed" should call this rather than comparing status itself.
func Succeeded(status, outcome string) bool { return follow.Succeeded(status, outcome) }

// IsTerminal reports whether a status label is one a run does not leave:
// completed, failed or cancelled. Paused is not terminal; it resumes.
func IsTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}
