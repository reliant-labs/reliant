package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The workflow_check_runs facade (internal/workflowcheck's Store): the
// GitHub check runs workflows' `check:` blocks declare. The queries are in
// internal/store/workflow_check_run_repo_ext.go.

// CreateWorkflowCheckRun records a firing's check; false when the firing
// already has a check of that name.
func (r *Repo) CreateWorkflowCheckRun(ctx context.Context, row *core.WorkflowCheckRun) (bool, error) {
	if row == nil || row.ID == "" || row.TriggerEventID == "" {
		return false, fmt.Errorf("workflow check run needs an id and a trigger event")
	}
	return store.InsertWorkflowCheckRun(ctx, r.ORM(), row)
}

// MarkWorkflowCheckRunsStarted stamps the checks of chatID's launch firing as
// started and makes them due.
func (r *Repo) MarkWorkflowCheckRunsStarted(ctx context.Context, chatID string, at time.Time) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.MarkWorkflowCheckRunsStarted(ctx, r.ORM(), chatID, at)
}

// RecordWorkflowCheckRunsEnded records how chatID's launch run ended on its
// firing's checks and makes them due.
func (r *Repo) RecordWorkflowCheckRunsEnded(ctx context.Context, chatID string, ending core.WorkflowCheckEnding, at time.Time) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.RecordWorkflowCheckRunsEnded(ctx, r.ORM(), chatID, ending, at)
}

// ClaimWorkflowCheckRuns leases up to max due, unfinished checks to holder
// until leaseUntil, bumping each one's lease generation.
func (r *Repo) ClaimWorkflowCheckRuns(ctx context.Context, holder string, now, leaseUntil time.Time, max int) ([]*core.WorkflowCheckRun, error) {
	if holder == "" {
		return nil, fmt.Errorf("workflow check run lease holder cannot be empty")
	}
	return store.ClaimWorkflowCheckRuns(ctx, r.ORM(), holder, now, leaseUntil, max)
}

// RenewWorkflowCheckRunLeases extends the leases holder still holds and
// returns the ids it renewed; a lease not returned is lost.
func (r *Repo) RenewWorkflowCheckRunLeases(ctx context.Context, holder string, leases []core.WorkflowCheckLease, until time.Time) ([]string, error) {
	if holder == "" {
		return nil, fmt.Errorf("workflow check run lease holder cannot be empty")
	}
	return store.RenewWorkflowCheckRunLeases(ctx, r.ORM(), holder, leases, until)
}

// ObserveWorkflowCheckRun reads a check's firing and the run it launched.
func (r *Repo) ObserveWorkflowCheckRun(ctx context.Context, triggerEventID string) (*core.WorkflowCheckObservation, error) {
	if triggerEventID == "" {
		return nil, core.ErrWorkflowCheckRunNotFound
	}
	return store.ObserveWorkflowCheckRun(ctx, r.ORM(), triggerEventID)
}

// SaveWorkflowCheckRunSync writes a sync's result and releases the lease,
// fenced on the lease the sync claimed (core.ErrWorkflowCheckLeaseLost when
// it is no longer the row's).
func (r *Repo) SaveWorkflowCheckRunSync(ctx context.Context, row *core.WorkflowCheckRun) error {
	if row == nil || row.ID == "" {
		return fmt.Errorf("workflow check run ID cannot be empty")
	}
	return store.SaveWorkflowCheckRunSync(ctx, r.ORM(), row)
}
