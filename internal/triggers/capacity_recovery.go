package triggers

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/store"
	"go.temporal.io/api/serviceerror"
)

type capacityStartRecovery interface {
	TriggerCapacityStartFenced(context.Context, store.TriggerCapacityExecution) (bool, error)
	FenceUnboundTriggerCapacityStart(context.Context, store.TriggerCapacityExecution) (bool, error)
	ReleaseUnboundTriggerCapacity(context.Context, store.TriggerCapacityExecution, string) (bool, error)
}

func recoverMissingCapacityStart(ctx context.Context, repo capacityRepository, descriptions capacityDescriptions, e store.TriggerCapacityExecution) error {
	recovery, ok := repo.(capacityStartRecovery)
	if !ok {
		return nil
	}
	fenced, err := recovery.TriggerCapacityStartFenced(ctx, e)
	if err != nil {
		return err
	}
	if !fenced {
		won, err := recovery.FenceUnboundTriggerCapacityStart(ctx, e)
		if err != nil {
			return err
		}
		if !won {
			return nil
		}
	}
	// Fence first, THEN re-read the external execution: a late accepted start
	// after the first NotFound must retain the permit rather than over-admit.
	desc, err := descriptions.DescribeWorkflowExecution(ctx, e.WorkflowID, "")
	if err != nil {
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			_, err = recovery.ReleaseUnboundTriggerCapacity(ctx, e, "start_fenced_and_absent")
			return err
		}
		return nil
	}
	if desc.GetWorkflowExecutionInfo() == nil {
		return nil
	}
	// A late acceptance is reconciled on the next pass using its memo. Never
	// infer that the fence alone stopped an already accepted execution.
	return nil
}
