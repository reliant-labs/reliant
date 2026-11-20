package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// BatchApprove approves every listed approval that is still pending.
func (s *Service) BatchApprove(
	ctx context.Context,
	req *reliantv1.BatchApproveRequest,
) (*reliantv1.BatchApproveResponse, error) {
	if len(req.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, approved, skipped, err := s.resolveApprovals(ctx, req.RequestIds, decision{approved: true, actionTaken: req.ActionTaken})
	if err != nil {
		return nil, err
	}
	return &reliantv1.BatchApproveResponse{
		Success:  true,
		Approved: int32(approved),
		Message:  batchMessage("Approved", approved, skipped),
		ChatId:   chatID,
	}, nil
}
