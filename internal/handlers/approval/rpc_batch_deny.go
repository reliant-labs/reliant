package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// BatchDeny denies every listed approval that is still pending.
func (s *Service) BatchDeny(
	ctx context.Context,
	req *reliantv1.BatchDenyRequest,
) (*reliantv1.BatchDenyResponse, error) {
	if len(req.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, denied, skipped, err := s.resolveApprovals(ctx, req.RequestIds, decision{denialReason: denialReasonOr(req.DenialReason, "User denied one or more approvals"), actionTaken: req.ActionTaken})
	if err != nil {
		return nil, err
	}
	return &reliantv1.BatchDenyResponse{
		Success: true,
		Denied:  int32(denied),
		Message: batchMessage("Denied", denied, skipped),
		ChatId:  chatID,
	}, nil
}
