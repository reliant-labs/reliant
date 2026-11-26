package triggers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
)

const capacityWaiting = "waiting for workflow capacity"

var errCapacityScopeChanged = errors.New("queued workflow capacity scope changed")

// errCapacitySuperseded is a waiting event a newer one of its key already
// replaced (`waiting: latest`). Its row is settled; the fire reports it.
var errCapacitySuperseded = errors.New(store.TriggerCapacitySuperseded)

// capacitySettled reports a prepareCapacity error that ends the fire with the
// event's stored (or a skipped) record rather than a retry.
func capacitySettled(err error) bool {
	return errors.Is(err, errCapacityScopeChanged) || errors.Is(err, errCapacitySuperseded)
}

// errCapacityExpired is a waiting event recorded before limits.ExpiredBefore:
// it must never launch. The fire settles it failed with capacityExpiredDetail,
// as the redriver's expiry sweep would.
var errCapacityExpired = errors.New(capacityExpiredDetail)

// capacityEvents is the one event read admission makes besides the capacity
// store: the enrolled row, by its dedupe key.
type capacityEvents interface {
	GetTriggerEventByDedupe(context.Context, core.TriggerEventKind, string) (*core.TriggerEvent, error)
}

type capacityDescriptions interface {
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// capacityPolicy remains trigger-agnostic: no provider identity or subject
// revision. It is the workflow's declared policy, nil for none; capacityLimit
// resolves it to a cap for one trigger.
func capacityPolicy(ctx context.Context, resolver WorkflowResolver, t *core.Trigger) (*reliantv1.WorkflowConcurrency, error) {
	if resolver == nil {
		return nil, nil
	}
	wf, err := resolveTriggerWorkflow(ctx, resolver, t)
	if err != nil {
		return nil, err
	}
	return wf.GetConcurrency(), nil
}

// capacityLimits resolves a workflow's concurrency policy to the caps for
// this trigger now; ok is false when the workflow declares no policy. A policy
// that caps only per key has no workflow-wide cap.
func capacityLimits(ctx context.Context, placer Placer, t *core.Trigger, policy *reliantv1.WorkflowConcurrency) (store.TriggerCapacityLimits, bool, error) {
	limit, ok, err := capacityLimit(ctx, placer, t, policy)
	if err != nil {
		return store.TriggerCapacityLimits{}, ok, err
	}
	perKey := policy.GetMaxRunningPerKey()
	if !ok {
		if perKey == 0 {
			return store.TriggerCapacityLimits{}, false, nil
		}
		limit = store.TriggerCapacityUnbounded
	}
	return store.TriggerCapacityLimits{MaxRunning: limit, MaxRunningPerKey: perKey}, true, nil
}

// capacityAdmission fixes a not-yet-enrolled event's place in its scope's
// queue, rendered against the same `trigger` root the run sees: its
// CapacityKey (the waiting: latest supersede group), CapacityRunningKey (the
// max_running_per_key group) and CapacityPriority. An enrolled event keeps
// what it enrolled with. Whatever cannot be rendered is logged and left unset:
// a key that does not render leaves the event FIFO, never superseded and held
// only by the workflow-wide cap, and a priority that does not evaluate is 0.
// Losing an ordering is safe; dropping an event is not.
func capacityAdmission(ctx context.Context, policy *reliantv1.WorkflowConcurrency, event *core.TriggerEvent, scopeOf func() triggerspec.Scope) {
	if event.CapacityOrder != nil {
		return
	}
	var scope *triggerspec.Scope
	render := func() triggerspec.Scope {
		if scope == nil {
			s := scopeOf()
			scope = &s
		}
		return *scope
	}
	latest := policy.GetWaiting() == reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST
	if latest || policy.GetMaxRunningPerKey() > 0 {
		key, err := triggerspec.RenderConcurrencyKey(policy.GetKey(), render())
		if err != nil {
			slog.WarnContext(ctx, "workflow capacity: concurrency key did not render; the event waits FIFO, is never superseded and counts only against the workflow-wide cap",
				"event_id", event.ID, "error", err)
		} else {
			if latest {
				event.CapacityKey = &key
			}
			if policy.GetMaxRunningPerKey() > 0 {
				event.CapacityRunningKey = &key
			}
		}
	}
	if policy.GetPriority() != "" {
		priority, err := triggerspec.CompileConcurrencyPriority(policy.GetPriority())
		var value int64
		if err == nil {
			value, err = priority.Evaluate(render())
		}
		if err != nil {
			slog.WarnContext(ctx, "workflow capacity: concurrency priority did not evaluate; the event waits at priority 0",
				"event_id", event.ID, "error", err)
			return
		}
		event.CapacityPriority = &value
	}
}

// capacityScope is the pool a trigger's capped runs share: its owner, project
// and resolved workflow ref.
func capacityScope(t *core.Trigger) (store.TriggerCapacityScope, error) {
	ref, err := workflowref.Parse(t.Workflow)
	if err != nil {
		return store.TriggerCapacityScope{}, err
	}
	return store.TriggerCapacityScope{UserID: t.UserID, ProjectID: t.ProjectID, WorkflowRef: ref.Key()}, nil
}

// capacityWake hands a freed or still-free slot straight to the next waiting
// event. Without it a waiting event is fired again only by the redriver, one
// queue head per scope per pass, so N free slots took N passes to fill and a
// queue of fast runs drained at one launch a minute however high max_running
// was set: the queue grew faster than it drained and every new event waited
// behind it.
type capacityWake struct {
	starter   WorkflowStarter
	taskQueue string
}

// wakeNext fires the event the scope admits next, unless that is the firing
// itself (kind, dedupeKey).
//
// After a capped launch it hands a still-free slot on: that fire launches and
// wakes the next in turn, so a burst of free slots fills in one cascade, which
// stops once the scope is full (nothing is admissible).
//
// After a capped fire declines to wait, it hands the turn on: the declined
// event may be the redriver's head yet not admissible, because a higher
// priority firing arrived after the redriver's cutoff or its own key is at its
// max_running_per_key cap, while another waiting event could run now. The
// redriver does not know the policy, so without this that event would wait
// until something else launched.
//
// Best effort: a failure is logged, and the redriver still fires the head on
// its next pass.
func (w *capacityWake) wakeNext(ctx context.Context, queue CapacityStore, t *core.Trigger, limits store.TriggerCapacityLimits, kind core.TriggerEventKind, dedupeKey string) {
	if w == nil || w.starter == nil || limits.MaxRunning == 0 {
		return
	}
	scope, err := capacityScope(t)
	if err != nil {
		return
	}
	next, err := queue.NextAdmissibleTriggerCapacityEvent(ctx, scope, limits)
	if err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot find the next waiting event; the redriver will fire it",
			"trigger_id", t.ID, "error", err)
		return
	}
	if next == nil || (next.Kind == kind && next.DedupeKey == dedupeKey) || next.TriggerID == nil {
		return
	}
	if err := refireEvent(ctx, w.starter, w.taskQueue, next, time.Now()); err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot fire the next waiting event; the redriver will",
			"event_id", next.ID, "error", err)
	}
}

func prepareCapacity(ctx context.Context, events capacityEvents, repo CapacityStore, descriptions capacityDescriptions, t *core.Trigger, event *core.TriggerEvent, limits store.TriggerCapacityLimits, cancel capacityCancel, spec *launch.Spec) error {
	if descriptions == nil {
		return errors.New("workflow capacity Temporal descriptions are unavailable")
	}
	scope, err := capacityScope(t)
	if err != nil {
		return err
	}
	if event.Outcome == core.TriggerEventPending {
		if event.CapacityOrder != nil && (event.CapacityProjectID == nil || event.CapacityWorkflowRef == nil || *event.CapacityProjectID != scope.ProjectID || *event.CapacityWorkflowRef != scope.WorkflowRef) {
			return errCapacityScopeChanged
		}
		superseded, err := repo.EnqueueTriggerCapacitySuperseding(ctx, event, scope)
		if err != nil {
			return err
		}
		traceSuperseded(ctx, events, t, superseded)
	}
	enrolled, err := events.GetTriggerEventByDedupe(ctx, event.Kind, event.DedupeKey)
	if err != nil {
		return err
	}
	if enrolled.Outcome == core.TriggerEventSkipped {
		return errCapacitySuperseded
	}
	// An expired event never launches. Admission refuses it too (under the
	// lock), so this only spares the launch attempt and settles it now.
	if enrolled.Outcome == core.TriggerEventPending && !limits.ExpiredBefore.IsZero() && enrolled.CreatedAt.Before(limits.ExpiredBefore) {
		return errCapacityExpired
	}
	// concurrency.cancel_running: stop the key's older running run first, so
	// the reconcile below sees it closed and frees its slot (capacity_cancel.go).
	cancel.supersede(ctx, repo, descriptions, scope, enrolled)
	// Reconcile only immutable chains in this scope. A missing, mismatched or
	// unavailable description holds its slot; chat status is never release evidence.
	occupied, err := repo.ListTriggerCapacityExecutions(ctx, scope)
	if err != nil {
		return err
	}
	reconcileCapacityExecutions(ctx, repo, descriptions, occupied)
	previous := spec.Guard
	spec.Guard = func(ctx context.Context) (string, error) {
		// A per-member policy on a pool with no member able to take work
		// resolves to 0: wait, as for a full scope.
		if limits.MaxRunning == 0 {
			return capacityWaiting, nil
		}
		allowed, err := repo.CanLaunchTriggerCapacity(ctx, enrolled.ID, scope, limits)
		if err != nil {
			return "", err
		}
		if !allowed {
			return capacityWaiting, nil
		}
		if previous != nil {
			return previous(ctx)
		}
		return "", nil
	}
	spec.RecordExecution = repo.BindTriggerCapacityExecution
	spec.PrepareExecution = repo.PrepareTriggerCapacityExecution
	return nil
}

// reconcileCapacityExecutions frees the slots whose runs have finished. A row
// that cannot be reconciled is logged and keeps its slot; it never fails the
// fire, or one bad row would stop every launch in its scope.
func reconcileCapacityExecutions(ctx context.Context, repo CapacityStore, descriptions capacityDescriptions, occupied []store.TriggerCapacityExecution) {
	for _, execution := range occupied {
		if err := reconcileCapacityExecution(ctx, repo, descriptions, execution); err != nil {
			slog.WarnContext(ctx, "workflow capacity: cannot reconcile an occupied slot; it stays held",
				"event_id", execution.EventID, "workflow_id", execution.WorkflowID, "error", err)
		}
	}
}

func reconcileCapacityExecution(ctx context.Context, repo CapacityStore, descriptions capacityDescriptions, execution store.TriggerCapacityExecution) error {
	for _, execution := range []store.TriggerCapacityExecution{execution} {
		if execution.WorkflowID == "" {
			continue
		}
		// Only ABSENCE needs the start-attempt bound: a start still in flight
		// looks missing, or superseded, until it lands. A chain Temporal shows
		// is evidence at once, so a finished run frees its slot on the next
		// pass rather than core.StartAttemptBound after its launch.
		desc, err := descriptions.DescribeWorkflowExecution(ctx, execution.WorkflowID, "")
		if err != nil {
			var missing *serviceerror.NotFound
			if !errors.As(err, &missing) || !execution.StartAttemptAged {
				// An outage is uncertainty, and so is a young start: hold the
				// slot and look again on the next fire.
				continue
			}
			if execution.FirstRunID == "" {
				if err := recoverMissingCapacityStart(ctx, repo, descriptions, execution); err != nil {
					return err
				}
				continue
			}
			// A bound chain Temporal no longer knows has closed and aged past
			// namespace retention: Temporal deletes only closed histories. Holding
			// it would wedge the scope forever, so release it.
			if err := repo.ReleaseTriggerCapacityExecution(ctx, execution); err != nil {
				return fmt.Errorf("release workflow capacity: %w", err)
			}
			continue
		}
		info := desc.GetWorkflowExecutionInfo()
		if info == nil || info.GetExecution().GetWorkflowId() != execution.WorkflowID || info.GetFirstRunId() == "" {
			continue
		}
		if execution.FirstRunID == "" {
			var memoEvent string
			payload := info.GetMemo().GetFields()["trigger_event_id"]
			if payload == nil || converter.GetDefaultDataConverter().FromPayload(payload, &memoEvent) != nil || memoEvent != execution.EventID {
				if !execution.StartAttemptAged {
					// The admitted start may still land behind this chain.
					continue
				}
				fenced, err := repo.TriggerCapacityStartFenced(ctx, execution)
				if err != nil {
					return err
				}
				if fenced {
					if _, err := repo.ReleaseUnboundTriggerCapacity(ctx, execution, "superseded_before_binding"); err != nil {
						return err
					}
				}
				continue
			}
			if err := repo.BindTriggerCapacityExecution(ctx, execution.EventID, execution.WorkflowID, info.GetFirstRunId()); err != nil {
				return err
			}
			execution.FirstRunID = info.GetFirstRunId()
		}
		// Temporal permits one open execution per workflow ID. A different
		// current chain therefore proves the admitted chain is no longer open;
		// this is how a later human turn may reuse the chat without pinning the
		// automation's slot forever. Missing history/errors remain uncertainty.
		if info.GetFirstRunId() != execution.FirstRunID {
			if err := repo.ReleaseClosedTriggerCapacityExecution(ctx, execution); err != nil {
				return err
			}
			continue
		}
		switch info.GetStatus() {
		case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
			if err := repo.ReleaseClosedTriggerCapacityExecution(ctx, execution); err != nil {
				return fmt.Errorf("release workflow capacity: %w", err)
			}
		}
	}
	return nil
}

// traceSuperseded opens a trigger.fire span for each waiting event an
// enrollment superseded (`waiting: latest`): they were settled in bulk, by
// another event's firing, which ctx's span is and each links to. Each names
// its own trigger, read through repository when it is not t; one that cannot
// be read is named by none.
func traceSuperseded(ctx context.Context, repository any, t *core.Trigger, superseded []*core.TriggerEvent) {
	if len(superseded) == 0 {
		return
	}
	triggers := map[string]*core.Trigger{t.ID: t}
	reader, _ := repository.(triggerReader)
	for _, ev := range superseded {
		traceSettled(ctx, settledTrigger(ctx, reader, triggers, ev), ev, ev.Outcome, ev.OutcomeDetail)
	}
}
