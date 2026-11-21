package approval

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// BatchApprove approves every listed approval that is still pending.
func (s *Service) BatchApprove(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchApproveRequest],
) (*connect.Response[reliantv1.BatchApproveResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, approved, skipped, err := s.resolveApprovals(ctx, req.Msg.RequestIds, decision{approved: true, actionTaken: req.Msg.ActionTaken})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.BatchApproveResponse{
		Success:  true,
		Approved: int32(approved),
		Message:  batchMessage("Approved", approved, skipped),
		ChatId:   chatID,
	}), nil
}
