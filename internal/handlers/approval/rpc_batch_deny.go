package approval

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// BatchDeny denies every listed approval that is still pending.
func (s *Service) BatchDeny(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchDenyRequest],
) (*connect.Response[reliantv1.BatchDenyResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, denied, skipped, err := s.resolveApprovals(ctx, req.Msg.RequestIds, decision{denialReason: denialReasonOr(req.Msg.DenialReason, "User denied one or more approvals"), actionTaken: req.Msg.ActionTaken})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.BatchDenyResponse{
		Success: true,
		Denied:  int32(denied),
		Message: batchMessage("Denied", denied, skipped),
		ChatId:  chatID,
	}), nil
}
