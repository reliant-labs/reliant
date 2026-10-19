package provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
)

// DeleteProviderAccount removes an account and its stored credential. Priorities re-densify and
// threads that were assigned to it move to another account at their next turn.
func (s *Service) DeleteProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.DeleteProviderAccountRequest],
) (*connect.Response[pb.DeleteProviderAccountResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetAccountId() == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	acct, err := s.find(ctx, userID, req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	if err := s.deps.Store.DeleteProviderAccount(ctx, userID, acct.ID); err != nil {
		return nil, wrapAccountErr(err)
	}
	s.afterChange(ctx, userID, acct.Provider)
	method := "oauth"
	if acct.AuthKind == "api_key" {
		method = "api_key"
	}
	analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider: acct.Provider, Action: "disconnected", AuthMethod: method,
	})
	return connect.NewResponse(&pb.DeleteProviderAccountResponse{}), nil
}
