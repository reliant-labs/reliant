package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// NodeObservation is an alias to the shared core node-observation model.
type NodeObservation = core.NodeObservation

// NodeObservationWrite is an alias to how a RecordNodeObservation ended.
type NodeObservationWrite = core.NodeObservationWrite

// The node observation constants, re-exported for the Repo's callers.
const (
	NodeObservationSourceWorkflow = core.NodeObservationSourceWorkflow
	NodeObservationSourceModel    = core.NodeObservationSourceModel
	// NodeObservationSourceReconciler is the reconciler's own record.
	NodeObservationSourceReconciler = core.NodeObservationSourceReconciler
	NodeObservationStored           = core.NodeObservationStored
	NodeObservationDuplicate        = core.NodeObservationDuplicate
	NodeObservationCapped           = core.NodeObservationCapped
)

// RecordNodeObservation stores what one node attempt's observe: block
// recorded, at most maxPerRun rows a run (store.InsertNodeObservation). A
// second write for the same node attempt is a no-op.
func (r *Repo) RecordNodeObservation(ctx context.Context, obs *NodeObservation, maxPerRun int) (NodeObservationWrite, error) {
	if obs == nil {
		return 0, fmt.Errorf("node observation cannot be nil")
	}
	return store.InsertNodeObservation(ctx, r.ORM(), obs, maxPerRun)
}

// ListWorkflowNodeObservations returns the observations recorded in one
// workflow, oldest first.
func (r *Repo) ListWorkflowNodeObservations(ctx context.Context, workflowID string) ([]*NodeObservation, error) {
	return store.ListWorkflowNodeObservations(ctx, r.ORM(), workflowID)
}
