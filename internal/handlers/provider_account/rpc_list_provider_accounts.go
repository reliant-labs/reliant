package provider_account

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListProviderAccounts returns the caller's accounts grouped by provider in priority order, each
// with its derived status and latest usage windows.
func (s *Service) ListProviderAccounts(
	ctx context.Context,
	_ *connect.Request[pb.ListProviderAccountsRequest],
) (*connect.Response[pb.ListProviderAccountsResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	limits, err := s.deps.Store.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.ListProviderAccountsResponse{
		Accounts: protoAll(accounts, limits, "", time.Now()),
	}), nil
}
