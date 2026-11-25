// Copyright (c) 2025 Reliant Labs. All rights reserved.
package workflow

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// WokenStatusStore is what MarkWokenRunActive writes through.
type WokenStatusStore interface {
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
}

// FacadeWorkflows adapts a store that reads workflows as GetWorkflow (the
// *db.Repo facade) to WorkflowLoader, for StatusBeforeWake callers that do
// not yet take a WorkflowLoader role of their own.
type FacadeWorkflows struct {
	Repo interface {
		GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	}
}

// LoadWorkflow reads the row through the wrapped store's GetWorkflow.
func (f FacadeWorkflows) LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error) {
	return f.Repo.GetWorkflow(ctx, id)
}

// StatusBeforeWake is the workflow row's status read before the caller wakes
// its run (a signal, a reset), for MarkWokenRunActive. ok is false when it
// could not be read; the caller then writes nothing after the wake.
func StatusBeforeWake(ctx context.Context, workflows WorkflowLoader, workflowID string) (status db.WorkflowStatus, ok bool) {
	wf, err := workflows.LoadWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return db.WorkflowStatus{}, false
	}
	return wf.Status, true
}

// MarkWokenRunActive records a run the API has just woken as running — only
// while its row still holds before, the status read before the wake.
//
// The write follows the wake, and the woken run writes its own status from
// the moment it wakes: a resumed run with one short turn left can finish and
// write COMPLETED before the API gets here. An unconditional write of active
// then overwrote that terminal status, so the row read ACTIVE over a closed
// run — a chat that looked as if it were running forever until the
// reconciler next compared it with Temporal (e2e story 05, 2026-10-26). As a
// compare-and-swap it loses to anything the run wrote, which is the order
// that is true.
//
// Best-effort, as the writes it replaces were: the wake has already happened,
// and the run's own status writes and the reconciler converge the row.
func MarkWokenRunActive(ctx context.Context, store WokenStatusStore, workflowID string, before db.WorkflowStatus, ok bool) {
	if !ok || before == db.Active() {
		return
	}
	if _, err := store.CompareAndSwapWorkflowStatus(ctx, workflowID, db.Active(), before); err != nil {
		logging.WarnContext(ctx, "[PauseService] Woke the run but could not mark it running; its own status write or the reconciler corrects it",
			"workflowID", workflowID, "statusBeforeWake", before.Label(), "error", err)
	}
}
