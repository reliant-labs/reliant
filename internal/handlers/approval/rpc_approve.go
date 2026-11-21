package approval

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Approve approves a pending approval request
func (s *Service) Approve(
	ctx context.Context,
	req *connect.Request[reliantv1.ApproveRequest],
) (*connect.Response[reliantv1.ApproveResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, _, err := s.resolveApprovals(ctx, []string{req.Msg.RequestId}, decision{approved: true, actionTaken: req.Msg.ActionTaken, connectionID: req.Msg.GetConnectionId()}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.ApproveResponse{
		Success: true,
		Message: "Request approved",
	}), nil
}
