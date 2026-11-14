package org_provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListOrgProviderAccounts returns the org's accounts grouped by provider in priority order: whole
// to an owner or admin (read fresh), narrowed to provider, label and status for a member.
func (s *Service) ListOrgProviderAccounts(
	ctx context.Context,
	req *connect.Request[pb.ListOrgProviderAccountsRequest],
) (*connect.Response[pb.ListOrgProviderAccountsResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	_, isAdmin, err := reader(ctx, orgID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.deps.Store.ListOwnedProviderAccounts(ctx, core.OrgAccountOwner(orgID))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	v, err := s.load(ctx, orgID, isAdmin)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ListOrgProviderAccountsResponse{
		Accounts:      v.protoAll(accounts, ""),
		CallerIsAdmin: isAdmin,
		AddingEnabled: s.addingEnabled(),
	}), nil
}
