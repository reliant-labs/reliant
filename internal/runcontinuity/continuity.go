// Copyright (c) 2025 Reliant Labs

// Package runcontinuity proves only Temporal-internal continue-as-new ancestry.
// The immutable starting execution comes from authenticated server enrollment,
// never an event body, prompt, chat's current pointer, or latest-run lookup.
//
//forge:exclude-contract: pure bounded proof over consumer-owned Temporal history reader; no app component
package runcontinuity

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
)

// ErrUnverified means history cannot prove this is the same logical author.
var ErrUnverified = errors.New("unverified workflow continuity")

// Execution is an exact Temporal workflow/run identity. Neither field may empty.
type Execution struct {
	WorkflowID string
	RunID      string
}

// Snapshot contains provider-authenticated first and close events, not caller
// claims. Running is true only when exact Describe says RUNNING and no close
// event was returned. A retained terminal run must have its close event.
type Snapshot struct {
	Start   *historypb.WorkflowExecutionStartedEventAttributes
	Close   *historypb.HistoryEvent
	Running bool
}

// Reader reads exact-run authenticated histories, bounded by the context.
type Reader interface {
	Snapshot(context.Context, Execution) (Snapshot, error)
}

// Edge is an immutable bidirectionally verified internal continuation.
type Edge struct {
	From Execution
	To   Execution
}

// Result identifies the only verified recipient, or a terminal original author.
// Terminal never means a new/replacement execution should be launched.
type Result struct {
	Original   Execution
	Current    Execution
	FirstRunID string
	Edges      []Edge
	Live       bool
	Terminal   enumspb.WorkflowExecutionStatus
}

// Resolve follows at most 64 authenticated edges with an overall ten-second
// deadline (or caller's earlier deadline), rejecting reset/retry/cron/cycles.
// FirstRunID alone is NOT a proof: Temporal shares it with retry/cron/reset.
func Resolve(ctx context.Context, reader Reader, original Execution) (Result, error) {
	if reader == nil || !valid(original) {
		return Result{}, ErrUnverified
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := Result{Original: original, Current: original}
	seen := map[string]bool{}
	var predecessor Snapshot
	var expectedPrevious string
	var workflowType, taskQueue string
	for hop := 0; hop <= 64; hop++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		current := result.Current
		if seen[current.RunID] {
			return Result{}, fmt.Errorf("%w: continuation cycle", ErrUnverified)
		}
		seen[current.RunID] = true
		snapshot, err := reader.Snapshot(ctx, current)
		if err != nil {
			return Result{}, err
		}
		start := snapshot.Start
		if start == nil || start.GetWorkflowId() != current.WorkflowID || start.GetOriginalExecutionRunId() != current.RunID || start.GetFirstExecutionRunId() == "" || start.GetWorkflowType().GetName() == "" || start.GetTaskQueue().GetName() == "" {
			return Result{}, fmt.Errorf("%w: missing or reset start identity", ErrUnverified)
		}
		if hop == 0 {
			result.FirstRunID = start.GetFirstExecutionRunId()
			workflowType = start.GetWorkflowType().GetName()
			taskQueue = start.GetTaskQueue().GetName()
		} else {
			close := predecessor.Close.GetWorkflowExecutionContinuedAsNewEventAttributes()
			if close == nil || start.GetContinuedExecutionRunId() != expectedPrevious || start.GetFirstExecutionRunId() != result.FirstRunID || !workflowInitiated(start.GetInitiator()) || start.GetWorkflowType().GetName() != workflowType || start.GetTaskQueue().GetName() != taskQueue || close.GetWorkflowType().GetName() != workflowType || close.GetTaskQueue().GetName() != taskQueue {
				return Result{}, fmt.Errorf("%w: successor does not authenticate predecessor", ErrUnverified)
			}
		}
		if snapshot.Close == nil {
			if !snapshot.Running {
				return Result{}, fmt.Errorf("%w: closed history unavailable", ErrUnverified)
			}
			result.Live = true
			result.Terminal = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
			return result, nil
		}
		if snapshot.Close.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW {
			terminal := terminalStatus(snapshot.Close.GetEventType())
			if terminal == enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED {
				return Result{}, ErrUnverified
			}
			result.Terminal = terminal
			return result, nil
		}
		close := snapshot.Close.GetWorkflowExecutionContinuedAsNewEventAttributes()
		if hop == 64 || close == nil || !workflowInitiated(close.GetInitiator()) || close.GetNewExecutionRunId() == "" {
			return Result{}, fmt.Errorf("%w: continuation limit or initiator", ErrUnverified)
		}
		next := Execution{WorkflowID: current.WorkflowID, RunID: close.GetNewExecutionRunId()}
		result.Edges = append(result.Edges, Edge{From: current, To: next})
		predecessor = snapshot
		expectedPrevious = current.RunID
		result.Current = next
	}
	return Result{}, ErrUnverified
}

func valid(e Execution) bool {
	return e.WorkflowID != "" && e.RunID != "" && len(e.WorkflowID) <= 255 && len(e.RunID) <= 255
}

// The pinned Go SDK v1.50.0 does not populate CAN command Initiator (zero).
// Temporal copies it into close/start history. Zero is accepted ONLY alongside
// the actual CAN close + successor back-reference; retries/crons are refused.
func workflowInitiated(i enumspb.ContinueAsNewInitiator) bool {
	return i == enumspb.CONTINUE_AS_NEW_INITIATOR_UNSPECIFIED || i == enumspb.CONTINUE_AS_NEW_INITIATOR_WORKFLOW
}
func terminalStatus(t enumspb.EventType) enumspb.WorkflowExecutionStatus {
	switch t {
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED:
		return enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED:
		return enumspb.WORKFLOW_EXECUTION_STATUS_FAILED
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED:
		return enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED:
		return enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TIMED_OUT:
		return enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT
	default:
		return enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED
	}
}
