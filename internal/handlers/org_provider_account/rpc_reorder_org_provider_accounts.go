package org_provider_account

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ReorderOrgProviderAccounts sets the order of one provider's org accounts (the In-order sequence
// of the org's tier). account_ids must be exactly the org's accounts of that provider.
func (s *Service) ReorderOrgProviderAccounts(
	ctx context.Context,
	req *connect.Request[pb.ReorderOrgProviderAccountsRequest],
) (*connect.Response[pb.ReorderOrgProviderAccountsResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	if provider == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider is required"))
	}
	if _, err := admin(ctx, orgID); err != nil {
		return nil, err
	}
	owner := core.OrgAccountOwner(orgID)
	if err := s.deps.Store.ReorderOwnedProviderAccounts(ctx, owner, provider, req.Msg.GetAccountIds()); err != nil {
		return nil, svcerr.Wrap(err)
	}
	accounts, err := s.deps.Store.ListOwnedProviderAccounts(ctx, owner)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	v, err := s.load(ctx, orgID, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ReorderOrgProviderAccountsResponse{Accounts: v.protoAll(accounts, provider)}), nil
}
