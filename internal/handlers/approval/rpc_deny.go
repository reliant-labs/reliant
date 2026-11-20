package approval

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Deny denies a pending approval request
func (s *Service) Deny(
	ctx context.Context,
	req *connect.Request[reliantv1.DenyRequest],
) (*connect.Response[reliantv1.DenyResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, _, err := s.resolveApprovals(ctx, []string{req.Msg.RequestId}, decision{denialReason: denialReasonOr(req.Msg.DenialReason, "User denied this approval"), actionTaken: req.Msg.ActionTaken}); err != nil {
		return nil, err
	}
	logging.InfoContext(ctx, "Denied request and signalled workflow", "requestID", req.Msg.RequestId)
	return connect.NewResponse(&reliantv1.DenyResponse{
		Success: true,
		Message: "Request denied",
	}), nil
}
