// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Names registered with Temporal.
const (
	DispatchWorkflowName = "RunEventDispatchWorkflow"
	DispatchActivityName = "DispatchRunEvent"
)

// DispatchWorkflowID is the dispatch workflow's id for a run event. It is
// deterministic, so a relay that starts the same event twice — after a crash
// between the start and the dispatched stamp — attaches instead of
// dispatching twice.
func DispatchWorkflowID(runEventID string) string { return "run-event-" + runEventID }

// DispatchInput is the dispatch workflow's and activity's input.
type DispatchInput struct {
	RunEventID string `json:"run_event_id"`
}

// DispatchOutput reports what one dispatch did, for the Temporal UI.
type DispatchOutput struct {
	Results []Result `json:"results,omitempty"`
}

// RunEventDispatchWorkflow gives one run event's dispatch a durable identity
// and Temporal's retry policy. All the work is in the activity.
func RunEventDispatchWorkflow(ctx workflow.Context, input DispatchInput) (*DispatchOutput, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    10,
		},
	})
	var out DispatchOutput
	if err := workflow.ExecuteActivity(ctx, DispatchActivityName, input).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RunEventReader loads the event a dispatch is for.
type RunEventReader interface {
	GetRunEvent(ctx context.Context, id string) (*core.RunEvent, error)
}

// Activity is the DispatchRunEvent activity's receiver.
type Activity struct {
	events     RunEventReader
	dispatcher *Dispatcher
}

// NewActivity builds the dispatch activity.
func NewActivity(events RunEventReader, dispatcher *Dispatcher) *Activity {
	return &Activity{events: events, dispatcher: dispatcher}
}

// Dispatch is the DispatchRunEvent activity.
func (a *Activity) Dispatch(ctx context.Context, input DispatchInput) (*DispatchOutput, error) {
	ev, err := a.events.GetRunEvent(ctx, input.RunEventID)
	if errors.Is(err, core.ErrRunEventNotFound) {
		// Pruned, or its chat was deleted (the row cascades). Nothing to do.
		return &DispatchOutput{}, nil
	}
	if err != nil {
		return nil, err
	}
	results, err := a.dispatcher.Dispatch(ctx, ev)
	if err != nil {
		return nil, err
	}
	return &DispatchOutput{Results: results}, nil
}

// RelayStore is the outbox surface the relay drives. *db.Repo satisfies it.
type RelayStore interface {
	ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error)
	MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error
	DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// WorkflowStarter starts a dispatch workflow. Satisfied by client.Client.
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
}

// Relay moves outbox rows into dispatch workflows. Several relays (one per
// worker) can run at once: rows are leased with SKIP LOCKED, and a row whose
// relay died mid-handoff is leased again once its lease expires.
type Relay struct {
	store     RelayStore
	starter   WorkflowStarter
	taskQueue string

	Interval  time.Duration // poll interval
	Lease     time.Duration // how long a claimed row is held before another relay may take it
	Batch     int           // rows per poll
	Retention time.Duration // how long a dispatched row is kept for inspection

	now func() time.Time
}

// NewRelay builds a relay with production defaults.
func NewRelay(store RelayStore, starter WorkflowStarter, taskQueue string) *Relay {
	return &Relay{
		store: store, starter: starter, taskQueue: taskQueue,
		Interval: 2 * time.Second, Lease: time.Minute, Batch: 50, Retention: 7 * 24 * time.Hour,
		now: time.Now,
	}
}

// Run drives the relay until ctx is done.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		if _, err := r.RelayOnce(ctx); err != nil && ctx.Err() == nil {
			logging.Warn("[workflowevent] relay pass failed", "error", err)
		}
		if r.now().Sub(lastPrune) > time.Hour {
			if n, err := r.store.DeleteDispatchedRunEventsBefore(ctx, r.now().Add(-r.Retention)); err != nil {
				logging.Warn("[workflowevent] pruning dispatched run events failed", "error", err)
			} else if n > 0 {
				logging.Info("[workflowevent] pruned dispatched run events", "count", n)
			}
			lastPrune = r.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RelayOnce hands every currently claimable row to its dispatch workflow and
// returns how many it handed off.
func (r *Relay) RelayOnce(ctx context.Context) (int, error) {
	now := r.now().UTC()
	rows, err := r.store.ClaimRunEvents(ctx, now, now.Add(r.Lease), r.Batch)
	if err != nil {
		return 0, err
	}
	handed := 0
	var errs []error
	for _, ev := range rows {
		if err := r.start(ctx, ev.ID); err != nil {
			// Left claimed; the lease expires and a later pass retries it.
			errs = append(errs, err)
			continue
		}
		if err := r.store.MarkRunEventDispatched(ctx, ev.ID, r.now().UTC()); err != nil {
			errs = append(errs, fmt.Errorf("mark run event %s dispatched: %w", ev.ID, err))
			continue
		}
		handed++
	}
	return handed, errors.Join(errs...)
}

func (r *Relay) start(ctx context.Context, runEventID string) error {
	_, err := r.starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        DispatchWorkflowID(runEventID),
		TaskQueue: r.taskQueue,
		// A run event is dispatched once. If an earlier relay pass already
		// started (or finished) this workflow, the start is refused with
		// WorkflowExecutionAlreadyStarted, which is success here. A dispatch
		// that exhausted its retries stays failed and visible in Temporal
		// rather than being silently re-run by the next pass.
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionTimeout: 30 * time.Minute,
	}, DispatchWorkflowName, DispatchInput{RunEventID: runEventID})
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &already) {
		return fmt.Errorf("start dispatch for run event %s: %w", runEventID, err)
	}
	return nil
}
