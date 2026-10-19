package provider_account

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
)

// RefreshProviderAccountUsage reads one account's usage now, for Settings →
// Providers on open and its refresh button. The server throttles it to one
// provider call per account per minute (accountlimits.UsageFloor): inside that
// window it answers the stored state with throttled set. A usage endpoint that
// fails is not an error: the account keeps the numbers response headers give
// it, and the failure is on the returned account's usage_error.
//
// Another user's account id is NotFound, never PermissionDenied, so ids cannot
// be enumerated.
func (s *Service) RefreshProviderAccountUsage(
	ctx context.Context,
	req *connect.Request[pb.RefreshProviderAccountUsageRequest],
) (*connect.Response[pb.RefreshProviderAccountUsageResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	accountID := req.Msg.GetAccountId()
	if accountID == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	if s.deps.UsageRefresher == nil {
		return nil, svcerr.Wrap(svcerr.Unavailable("usage refresh is not available on this server"))
	}

	res, err := s.deps.UsageRefresher.RefreshUsage(ctx, userID, accountID)
	if err != nil {
		if errors.Is(err, core.ErrProviderAccountNotFound) {
			return nil, svcerr.Wrap(svcerr.NotFound("provider account"))
		}
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.RefreshProviderAccountUsageResponse{
		Account:   accountlimits.ProtoAccount(res.Account, res.Limits, time.Now()),
		Throttled: res.Throttled,
	}), nil
}
