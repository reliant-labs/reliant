package provider_account

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReorderProviderAccounts sets the priority order of one provider's accounts. account_ids must be
// exactly that provider's accounts.
func (s *Service) ReorderProviderAccounts(
	ctx context.Context,
	req *pb.ReorderProviderAccountsRequest,
) (*pb.ReorderProviderAccountsResponse, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	provider := req.GetProvider()
	if provider == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider is required"))
	}
	if err := s.deps.Store.ReorderProviderAccounts(ctx, userID, provider, req.GetAccountIds()); err != nil {
		return nil, wrapAccountErr(err)
	}
	s.afterChange(ctx, userID, provider)
	accounts, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	limits, err := s.deps.Store.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return &pb.ReorderProviderAccountsResponse{
		Accounts: protoAll(accounts, limits, provider, time.Now()),
	}, nil
}
