package org_provider_account

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReleaseOrgProviderSeat stops the org paying for one member's seat: the account is the member's
// personal one again. The managed Reliant key minted in the org is not a seat. Never gated.
func (s *Service) ReleaseOrgProviderSeat(
	ctx context.Context,
	req *pb.ReleaseOrgProviderSeatRequest,
) (*pb.ReleaseOrgProviderSeatResponse, error) {
	orgID, err := requireOrgID(req.GetOrgId())
	if err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Store.ReleaseOrgSeat(ctx, orgID, req.GetAccountId(), userID); err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "org provider seat released", "org_id", orgID, "account_id", req.GetAccountId(), "user_id", userID)
	return &pb.ReleaseOrgProviderSeatResponse{}, nil
}
