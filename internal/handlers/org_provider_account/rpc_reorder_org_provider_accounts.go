package org_provider_account

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ReorderOrgProviderAccounts sets the order of one provider's org accounts (the In-order sequence
// of the org's tier). account_ids must be exactly the org's accounts of that provider.
func (s *Service) ReorderOrgProviderAccounts(
	ctx context.Context,
	req *pb.ReorderOrgProviderAccountsRequest,
) (*pb.ReorderOrgProviderAccountsResponse, error) {
	orgID, err := requireOrgID(req.GetOrgId())
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(req.GetProvider())
	if provider == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider is required"))
	}
	if _, err := admin(ctx, orgID); err != nil {
		return nil, err
	}
	owner := core.OrgAccountOwner(orgID)
	if err := s.deps.Store.ReorderOwnedProviderAccounts(ctx, owner, provider, req.GetAccountIds()); err != nil {
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
	return &pb.ReorderOrgProviderAccountsResponse{Accounts: v.protoAll(accounts, provider)}, nil
}
