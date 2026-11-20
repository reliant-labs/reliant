package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Deny denies a pending approval request
func (s *Service) Deny(
	ctx context.Context,
	req *reliantv1.DenyRequest,
) (*reliantv1.DenyResponse, error) {
	if req.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, _, err := s.resolveApprovals(ctx, []string{req.RequestId}, decision{denialReason: denialReasonOr(req.DenialReason, "User denied this approval"), actionTaken: req.ActionTaken}); err != nil {
		return nil, err
	}
	logging.InfoContext(ctx, "Denied request and signalled workflow", "requestID", req.RequestId)
	return &reliantv1.DenyResponse{
		Success: true,
		Message: "Request denied",
	}, nil
}
