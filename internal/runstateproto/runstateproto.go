// Copyright (c) 2025 Reliant Labs

// Package runstateproto maps a workflow run's lifecycle state, stop reason and
// outcome (core.WorkflowState, core.WorkflowStopReason, core.RunOutcome*) onto
// the proto enums RunService and ChatService report.
//
//forge:exclude-contract: pure enum mapping; no I/O, state or time
package runstateproto

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// State is a run's lifecycle state as the proto enum.
func State(state core.WorkflowState) reliantv1.WorkflowState {
	switch state {
	case core.WorkflowStatePending:
		return reliantv1.WorkflowState_WORKFLOW_STATE_PENDING
	case core.WorkflowStateActive:
		return reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE
	case core.WorkflowStateStopped:
		return reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED
	default:
		return reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED
	}
}

// StopReason is why a stopped run stopped, as the proto enum.
func StopReason(reason core.WorkflowStopReason) reliantv1.WorkflowStopReason {
	switch reason {
	case core.StopReasonCompleted:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED
	case core.StopReasonFailed:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED
	case core.StopReasonPaused:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED
	case core.StopReasonCancelled:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED
	default:
		return reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED
	}
}

// Outcome is how a run ended (a core.RunOutcome* value, "" for none), as the
// proto enum. An unknown value — one a newer release wrote — is unspecified.
func Outcome(outcome string) reliantv1.RunOutcome {
	switch outcome {
	case core.RunOutcomeSucceeded:
		return reliantv1.RunOutcome_RUN_OUTCOME_SUCCEEDED
	case core.RunOutcomeFailed:
		return reliantv1.RunOutcome_RUN_OUTCOME_FAILED
	case core.RunOutcomeFailureHandled:
		return reliantv1.RunOutcome_RUN_OUTCOME_FAILURE_HANDLED
	case core.RunOutcomeSkipped:
		return reliantv1.RunOutcome_RUN_OUTCOME_SKIPPED
	default:
		return reliantv1.RunOutcome_RUN_OUTCOME_UNSPECIFIED
	}
}
