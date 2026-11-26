package trigger

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetTrigger returns one trigger with its read-only projections.
func (s *Service) GetTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.GetTriggerRequest],
) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	out := s.render(ctx, trigger)
	changes, err := s.deps.TriggerWrites.TriggerChanges(ctx, trigger.UserID, trigger.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	for _, c := range changes {
		out.Changes = append(out.Changes, &reliantv1.TriggerChange{Id: c.ID, UserId: c.UserID, ChatId: c.ChatID, RunId: c.RunID, ToolCallId: c.ToolCallID, Operation: c.Operation, BeforeState: c.BeforeState, AfterState: c.AfterState, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), Approval: c.Approval, CapabilitiesAdded: c.CapabilitiesAdded, CapabilitiesRemoved: c.CapabilitiesRemoved})
	}
	return connect.NewResponse(&reliantv1.GetTriggerResponse{Trigger: out}), nil
}
