package org_provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// DeleteOrgProviderAccount removes an org account and destroys its sealed key; its grants, limit
// state and thread assignments go with it. Never gated: an admin can always stop the org's spend.
func (s *Service) DeleteOrgProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.DeleteOrgProviderAccountRequest],
) (*connect.Response[pb.DeleteOrgProviderAccountResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetAccountId() == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Store.DeleteOwnedProviderAccount(ctx, core.OrgAccountOwner(orgID), req.Msg.GetAccountId()); err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "org provider account deleted", "org_id", orgID, "account_id", req.Msg.GetAccountId(), "user_id", userID)
	return connect.NewResponse(&pb.DeleteOrgProviderAccountResponse{}), nil
}
