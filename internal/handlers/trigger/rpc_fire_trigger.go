package trigger

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// FireTrigger runs a trigger now. The firing is asynchronous: this returns the
// fire workflow id, and the outcome shows up in ListTriggerEvents.
func (s *Service) FireTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.FireTriggerRequest],
) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	// Run now launches with the activation's params, so one that is not set up
	// would start a run that fails its first input check. Say so up front.
	if err := s.requireSetupComplete(ctx, trigger); err != nil {
		return nil, err
	}
	if trigger.Kind.IsEventDriven() {
		fireID, err := s.fireInbound(ctx, trigger)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
	}
	if s.deps.Fires == nil {
		return nil, svcerr.Unavailable("the schedule backend is unavailable")
	}

	fireID, err := s.deps.Fires.StartManualFire(ctx, trigger.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("start fire failed"))
	}
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
}
