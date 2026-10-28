package triggers

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
)

const capacityWaiting = "waiting for workflow capacity"

var errCapacityScopeChanged = errors.New("queued workflow capacity scope changed")

type capacityRepository interface {
	EnqueueTriggerCapacity(context.Context, *core.TriggerEvent, store.TriggerCapacityScope) error
	CanLaunchTriggerCapacity(context.Context, string, store.TriggerCapacityScope, uint32) (bool, error)
	BindTriggerCapacityExecution(context.Context, string, string, string) error
	PrepareTriggerCapacityExecution(context.Context, string, string) error
	ListTriggerCapacityExecutions(context.Context, store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error)
	ReleaseTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error
	GetTriggerEventByDedupe(context.Context, core.TriggerEventKind, string) (*core.TriggerEvent, error)
}

type capacityDescriptions interface {
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// capacityPolicy remains trigger-agnostic: no provider identity or subject revision.
func capacityPolicy(ctx context.Context, resolver WorkflowResolver, t *core.Trigger) (uint32, error) {
	if resolver == nil {
		return 0, nil
	}
	wf, err := resolver.ResolveRunWorkflow(ctx, t.UserID, t.Workflow, t.ProjectID)
	if err != nil {
		return 0, err
	}
	return wf.GetConcurrency().GetMaxRunning(), nil
}

func prepareCapacity(ctx context.Context, repository any, descriptions capacityDescriptions, t *core.Trigger, event *core.TriggerEvent, n uint32, spec *launch.Spec) error {
	repo, ok := repository.(capacityRepository)
	if !ok {
		return errors.New("workflow capacity storage is unavailable")
	}
	if descriptions == nil {
		return errors.New("workflow capacity Temporal descriptions are unavailable")
	}
	ref, err := workflowref.Parse(t.Workflow)
	if err != nil {
		return err
	}
	poolRef := ref.Key()
	scope := store.TriggerCapacityScope{UserID: t.UserID, ProjectID: t.ProjectID, WorkflowRef: poolRef}
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
	// Reconcile only immutable chains in this scope. A missing, mismatched or
	// unavailable description holds its slot; chat status is never release evidence.
	occupied, err := repo.ListTriggerCapacityExecutions(ctx, scope)
	if err != nil {
		return err
	}
	if err := reconcileCapacityExecutions(ctx, repo, descriptions, occupied); err != nil {
		return err
	}
	previous := spec.Guard
	spec.Guard = func(ctx context.Context) (string, error) {
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

func reconcileCapacityExecutions(ctx context.Context, repo capacityRepository, descriptions capacityDescriptions, occupied []store.TriggerCapacityExecution) error {
	var errs []error
	for _, execution := range occupied {
		if err := reconcileCapacityExecution(ctx, repo, descriptions, execution); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func reconcileCapacityExecution(ctx context.Context, repo capacityRepository, descriptions capacityDescriptions, execution store.TriggerCapacityExecution) error {
	for _, execution := range []store.TriggerCapacityExecution{execution} {
		if execution.WorkflowID == "" {
			continue
		}
		desc, err := descriptions.DescribeWorkflowExecution(ctx, execution.WorkflowID, "")
		if err != nil {
			// Unknown state cannot release capacity. A namespace outage or
			// expired history requires explicit fenced operator resolution,
			// never an elapsed-time guess that might over-admit live work.
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
			if err := repo.ReleaseTriggerCapacityExecution(ctx, execution); err != nil {
				return err
			}
			continue
		}
		switch info.GetStatus() {
		case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
			if err := repo.ReleaseTriggerCapacityExecution(ctx, execution); err != nil {
				return fmt.Errorf("release workflow capacity: %w", err)
			}
		}
	}
	return nil
}
