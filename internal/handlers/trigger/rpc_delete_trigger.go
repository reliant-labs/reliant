package trigger

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DeleteTrigger removes the trigger and its schedule.
func (s *Service) DeleteTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteTriggerRequest],
) (*connect.Response[reliantv1.DeleteTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	// Drop the schedule FIRST. A schedule outliving its row keeps firing for a
	// trigger nobody can see or stop; a row outliving its schedule merely
	// stops firing, and the next Sync fixes it.
	if s.deps.Syncer != nil && syncs(trigger.Kind) {
		if err := s.deps.Syncer.Delete(ctx, trigger.ID); err != nil {
			return nil, triggerSyncError(err)
		}
	}
	if err := s.deps.Triggers.RemoveTrigger(ctx, trigger.ID); err != nil {
		return nil, triggerDBError("delete trigger", err)
	}
	if s.managed(trigger) {
		s.retireAlerting(ctx, trigger.ID)
	}
	s.grants.releaseIfUnused(ctx, trigger.UserID, trigger.DaemonID)
	return connect.NewResponse(&reliantv1.DeleteTriggerResponse{}), nil
}
