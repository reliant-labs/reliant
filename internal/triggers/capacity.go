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

type capacityRepository interface {
	EnqueueTriggerCapacity(context.Context, *core.TriggerEvent, store.TriggerCapacityScope) error
	CanLaunchTriggerCapacity(context.Context, string, store.TriggerCapacityScope, uint32) (bool, error)
	BindTriggerCapacityExecution(context.Context, string, string, string) error
	PrepareTriggerCapacityExecution(context.Context, string, string) error
	ListTriggerCapacityExecutions(context.Context, store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error)
	ReleaseTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error
	ReleaseClosedTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error
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

// capacityKey is the key a not-yet-enrolled event supersedes by under
// `waiting: latest`, rendered against the same `trigger` root the run sees;
// nil under FIFO. A key that cannot be rendered is logged and leaves the
// event FIFO: losing the coalescing is safe, dropping an event is not.
func capacityKey(ctx context.Context, policy *reliantv1.WorkflowConcurrency, event *core.TriggerEvent, scope func() triggerspec.Scope) *string {
	if policy.GetWaiting() != reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST || event.CapacityOrder != nil {
		return nil
	}
	key, err := triggerspec.RenderConcurrencyKey(policy.GetKey(), scope())
	if err != nil {
		slog.WarnContext(ctx, "workflow capacity: concurrency key did not render; the event waits FIFO and is never superseded",
			"event_id", event.ID, "error", err)
		return nil
	}
	return &key
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

// capacityQueue finds a scope's FIFO head. *db.Repo satisfies it.
type capacityQueue interface {
	NextWaitingTriggerCapacityEvent(context.Context, store.TriggerCapacityScope) (*core.TriggerEvent, error)
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

// wakeNext fires the scope's waiting head after a capped launch. That fire
// launches if a slot is still free, and wakes the next in turn, so a burst of
// free slots fills in one cascade; it declines (and the cascade stops) once
// the scope is full. Best effort: a failure is logged, and the redriver still
// fires the head on its next pass.
func (w *capacityWake) wakeNext(ctx context.Context, repo any, t *core.Trigger, launchedEventID string) {
	if w == nil || w.starter == nil {
		return
	}
	queue, ok := repo.(capacityQueue)
	if !ok {
		return
	}
	scope, err := capacityScope(t)
	if err != nil {
		return
	}
	next, err := queue.NextWaitingTriggerCapacityEvent(ctx, scope)
	if err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot find the next waiting event; the redriver will fire it",
			"trigger_id", t.ID, "error", err)
		return
	}
	if next == nil || next.ID == launchedEventID || next.TriggerID == nil {
		return
	}
	if err := refireEvent(ctx, w.starter, w.taskQueue, next, time.Now()); err != nil {
		slog.WarnContext(ctx, "workflow capacity: cannot fire the next waiting event; the redriver will",
			"event_id", next.ID, "error", err)
	}
}

func prepareCapacity(ctx context.Context, repository any, descriptions capacityDescriptions, t *core.Trigger, event *core.TriggerEvent, n uint32, spec *launch.Spec) error {
	repo, ok := repository.(capacityRepository)
	if !ok {
		return errors.New("workflow capacity storage is unavailable")
	}
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
		if err := repo.EnqueueTriggerCapacity(ctx, event, scope); err != nil {
			return err
		}
	}
	enrolled, err := repo.GetTriggerEventByDedupe(ctx, event.Kind, event.DedupeKey)
	if err != nil {
		return err
	}
	if enrolled.Outcome == core.TriggerEventSkipped {
		return errCapacitySuperseded
	}
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
		if n == 0 {
			return capacityWaiting, nil
		}
		allowed, err := repo.CanLaunchTriggerCapacity(ctx, enrolled.ID, scope, n)
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
func reconcileCapacityExecutions(ctx context.Context, repo capacityRepository, descriptions capacityDescriptions, occupied []store.TriggerCapacityExecution) {
	for _, execution := range occupied {
		if err := reconcileCapacityExecution(ctx, repo, descriptions, execution); err != nil {
			slog.WarnContext(ctx, "workflow capacity: cannot reconcile an occupied slot; it stays held",
				"event_id", execution.EventID, "workflow_id", execution.WorkflowID, "error", err)
		}
	}
}

func reconcileCapacityExecution(ctx context.Context, repo capacityRepository, descriptions capacityDescriptions, execution store.TriggerCapacityExecution) error {
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
				if recovery, ok := repo.(capacityStartRecovery); ok {
					fenced, err := recovery.TriggerCapacityStartFenced(ctx, execution)
					if err != nil {
						return err
					}
					if fenced {
						_, err = recovery.ReleaseUnboundTriggerCapacity(ctx, execution, "superseded_before_binding")
						if err != nil {
							return err
						}
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
