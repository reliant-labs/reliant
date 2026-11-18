// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"

	"go.temporal.io/api/workflowservice/v1"
)

// ExecutionDescriber is the one Temporal read DescribeExecution needs. A
// client.Client satisfies it, and so does every narrow test double that
// already fakes DescribeWorkflowExecution.
type ExecutionDescriber interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// DescribeExecution returns the raw DescribeWorkflowExecution response for a
// workflow execution (runID "" means the latest run).
//
// This is where reliant deliberately stays on the raw describe that sdk
// v1.51 deprecated (SA1019: "use DescribeWorkflow and its RawResponse"), and
// the only place that says so (reliant#2780). Every caller reads only the
// raw response — status, history length, memo payloads, run ids — and
// DescribeWorkflow is not a drop-in for that:
//
//   - it eagerly converts every search attribute through the default data
//     converter and PANICS on a typed payload that does not decode
//     (convertToTypedSearchAttributes). The callers are the reconciler's
//     sweep and pause/resume; one malformed attribute must not take down a
//     describe there, let alone the worker.
//   - it routes through the client's outbound interceptors, a second path
//     these reads have never taken.
//
// TestDescribeExecutionSurvivesAnUndecodableSearchAttribute pins both
// halves: this function returns the raw response, and DescribeWorkflow still
// panics on the same input. When an sdk release stops panicking, that test
// fails and says the migration can go ahead.
//
// Callers that declare their own narrow interface with this method
// (internal/runs, internal/triggers, internal/launch, the chat handler) read
// the same raw response for the same reason.
func DescribeExecution(ctx context.Context, c ExecutionDescriber, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	// Through ExecutionDescriber, not client.Client, so staticcheck does not
	// flag it and no caller needs a nolint: this comment is the justification.
	return c.DescribeWorkflowExecution(ctx, workflowID, runID)
}
