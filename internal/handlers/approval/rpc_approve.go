package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Approve approves a pending approval request
func (s *Service) Approve(
	ctx context.Context,
	req *reliantv1.ApproveRequest,
) (*reliantv1.ApproveResponse, error) {
	if req.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, _, err := s.resolveApprovals(ctx, []string{req.RequestId}, decision{approved: true, actionTaken: req.ActionTaken, connectionID: req.GetConnectionId()}); err != nil {
		return nil, err
	}
	return &reliantv1.ApproveResponse{
		Success: true,
		Message: "Request approved",
	}, nil
}
