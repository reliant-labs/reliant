package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
)

const capacityWaiting = "waiting for workflow capacity"

type capacityRepository interface {
	EnqueueTriggerCapacity(context.Context, *core.TriggerEvent, store.TriggerCapacityScope) error
	CanLaunchTriggerCapacity(context.Context, string, store.TriggerCapacityScope, uint32) (bool, error)
	BindTriggerCapacityExecution(context.Context, string, string, string) error
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
	scope := store.TriggerCapacityScope{UserID: t.UserID, ProjectID: t.ProjectID, WorkflowRef: strings.TrimPrefix(t.Workflow, "project://")}
	if event.Outcome == core.TriggerEventPending {
		if event.CapacityOrder != nil && (event.CapacityProjectID == nil || event.CapacityWorkflowRef == nil || *event.CapacityProjectID != scope.ProjectID || *event.CapacityWorkflowRef != scope.WorkflowRef) {
			return errors.New("queued workflow capacity scope changed")
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
	for _, execution := range occupied {
		if execution.WorkflowID == "" || execution.FirstRunID == "" {
			continue
		}
		desc, err := descriptions.DescribeWorkflowExecution(ctx, execution.WorkflowID, "")
		if err != nil {
			continue
		}
		info := desc.GetWorkflowExecutionInfo()
		if info == nil || info.GetExecution().GetWorkflowId() != execution.WorkflowID || info.GetFirstRunId() != execution.FirstRunID {
			continue
		}
		switch info.GetStatus() {
		case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
			if err := repo.ReleaseTriggerCapacityExecution(ctx, execution); err != nil {
				return fmt.Errorf("release workflow capacity: %w", err)
			}
		}
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
	return nil
}
