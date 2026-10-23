// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// ErrRunNotOpen is QueryOpenRun's answer for a workflow whose current run has
// closed: Temporal refused the query rather than replaying the run to answer
// it.
var ErrRunNotOpen = errors.New("temporal: the workflow's current run is not open")

// OpenRunQuerier is the one Temporal call QueryOpenRun needs. *client.Client
// satisfies it.
type OpenRunQuerier interface {
	QueryWorkflowWithOptions(ctx context.Context, request *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error)
}

// QueryOpenRun asks workflowID's CURRENT run a query, and only if that run is
// open. It is the only way reliant queries a workflow.
//
// Both halves are load-bearing, and leaving out either is what took the
// worker pool down on 2026-10-23
// (projects/reliant-labs/research/prod-deadline-sentry-2026-10-23.md):
//
//   - No run id. A stored run id goes stale every time a run continues as
//     new, and a query aimed at the stale run reaches a CLOSED execution.
//     The empty run id is Temporal's "the current run of this workflow".
//   - QUERY_REJECT_CONDITION_NOT_OPEN. Temporal answers a query on a closed
//     run by replaying its entire history on a worker — for a run that
//     continued as new, the whole ~37k-event history it handed off. The
//     caller's deadline does not cancel that replay: each one held two
//     worker cores for 13-17 minutes, starving every activity on the pod.
//     Rejected, the query costs one round trip to the history service.
//
// A rejected query returns ErrRunNotOpen. Callers that want the inputs a
// closed run ended with read them from Postgres instead (the chat's stored
// selection), never from the run.
func QueryOpenRun(ctx context.Context, q OpenRunQuerier, workflowID, queryType string, args ...any) (converter.EncodedValue, error) {
	resp, err := q.QueryWorkflowWithOptions(ctx, &client.QueryWorkflowWithOptionsRequest{
		WorkflowID:           workflowID,
		QueryType:            queryType,
		Args:                 args,
		QueryRejectCondition: enumspb.QUERY_REJECT_CONDITION_NOT_OPEN,
	})
	if err != nil {
		return nil, err
	}
	if resp.QueryRejected != nil {
		return nil, fmt.Errorf("%w (status %s)", ErrRunNotOpen, resp.QueryRejected.GetStatus())
	}
	if resp.QueryResult == nil {
		return nil, fmt.Errorf("temporal: query %q on %s returned no result", queryType, workflowID)
	}
	return resp.QueryResult, nil
}
