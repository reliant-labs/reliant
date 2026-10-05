// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ReconcileInterval is how often every activation's projection is checked
// against its workflow's declaration.
//
// One sweep, rather than a hook on each workflow save, because a declaration
// changes through more than the draft editor: a project's .reliant/workflows
// sync from the daemon, an import, a builtin upgraded by a deploy. A hook
// would cover only the writers someone remembered; the sweep covers all of
// them. The lag it allows is bounded and only affects ROUTING (which events
// reach an integration activation) and the Temporal cron — never what a fire
// does, because every fire re-reads the declaration.
const ReconcileInterval = time.Minute

// ProjectionStore is what the reconciler reads and writes. *db.Repo
// satisfies it.
type ProjectionStore interface {
	ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error)
	SetTriggerProjection(ctx context.Context, id string, p core.TriggerProjection) error
}

// ProjectionSyncer reconverges a trigger's Temporal schedule from its row.
// *Syncer satisfies it.
type ProjectionSyncer interface {
	Sync(ctx context.Context, triggerID string) error
}

// Reconciler keeps every activation's projection — the config and filter
// routing and the schedule syncer read — equal to its declaration.
type Reconciler struct {
	store     ProjectionStore
	workflows WorkflowResolver
	syncer    ProjectionSyncer
}

// NewReconciler builds a reconciler. syncer may be nil in a deployment with
// no schedule backend.
func NewReconciler(store ProjectionStore, workflows WorkflowResolver, syncer ProjectionSyncer) *Reconciler {
	return &Reconciler{store: store, workflows: workflows, syncer: syncer}
}

// ReconcileReport is what one pass found.
type ReconcileReport struct {
	Checked int
	Updated int
	Broken  int
}

// ReconcileAll checks every activation once. A broken activation is left as
// it is — its health and its firings report it, and re-projecting it from
// nothing would erase the schedule it last converged with. The error joins
// per-trigger failures; one bad workflow never stops the rest.
func (r *Reconciler) ReconcileAll(ctx context.Context) (ReconcileReport, error) {
	var report ReconcileReport
	activations, err := r.store.ListAllWorkflowTriggerActivations(ctx)
	if err != nil {
		return report, fmt.Errorf("list activations: %w", err)
	}
	// One request's worth of memoization: many activations of one workflow
	// parse it once per pass.
	workflows := NewCachedWorkflows(r.workflows)
	var errs []error
	for _, t := range activations {
		report.Checked++
		decl, err := ResolveDeclaration(ctx, workflows, t)
		if err != nil {
			var declErr *DeclarationError
			if errors.As(err, &declErr) {
				report.Broken++
				continue
			}
			errs = append(errs, fmt.Errorf("trigger %s: %w", t.ID, err))
			continue
		}
		p := decl.Projection()
		if bytes.Equal(p.Config, t.Config) && p.Filter == t.Filter {
			continue
		}
		if err := r.store.SetTriggerProjection(ctx, t.ID, p); err != nil {
			if errors.Is(err, core.ErrTriggerNotFound) {
				continue // deleted mid-pass
			}
			errs = append(errs, fmt.Errorf("trigger %s: write projection: %w", t.ID, err))
			continue
		}
		report.Updated++
		if r.syncer != nil && syncs(t.Kind) && !bytes.Equal(p.Config, t.Config) {
			if err := r.syncer.Sync(ctx, t.ID); err != nil {
				errs = append(errs, fmt.Errorf("trigger %s: reconverge schedule: %w", t.ID, err))
			}
		}
	}
	return report, errors.Join(errs...)
}

// Run sweeps every ReconcileInterval until ctx ends, starting immediately.
func (r *Reconciler) Run(ctx context.Context) {
	for {
		report, err := r.ReconcileAll(ctx)
		if err != nil {
			logging.Warn("trigger declaration reconcile pass had failures; the next pass retries",
				"checked", report.Checked, "updated", report.Updated, "broken", report.Broken, "error", err)
		} else if report.Updated > 0 {
			logging.Info("trigger declarations re-projected",
				"checked", report.Checked, "updated", report.Updated, "broken", report.Broken)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ReconcileInterval):
		}
	}
}

// syncs reports whether a kind is converged onto a Temporal schedule (an
// integration trigger only when it is polled, which Sync itself decides).
func syncs(kind core.TriggerKind) bool {
	return kind == core.TriggerKindSchedule || kind == core.TriggerKindIntegration
}
