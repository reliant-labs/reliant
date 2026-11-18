package org_provider_account

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// SetOrgProviderAccountGrants replaces the members an org account is granted to. Each must be a
// live member of the org now; a grant is bound to that membership (it stops counting if they
// leave, and a rejoin starts with none: routing compares it with the membership's start).
func (s *Service) SetOrgProviderAccountGrants(
	ctx context.Context,
	req *connect.Request[pb.SetOrgProviderAccountGrantsRequest],
) (*connect.Response[pb.SetOrgProviderAccountGrantsResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetUserIds()) > maxGrants {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("too many members in one grant"))
	}
	seen := map[string]bool{}
	users := make([]string, 0, len(req.Msg.GetUserIds()))
	for _, u := range req.Msg.GetUserIds() {
		u = strings.TrimSpace(u)
		if u == "" {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("user_ids must not hold an empty id"))
		}
		if !seen[u] {
			seen[u] = true
			users = append(users, u)
		}
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	acct, err := s.find(ctx, orgID, req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		switch err := orgs.RequireMember(ctx, u, orgID); {
		case err == nil:
		case svcerr.IsPermissionDenied(err):
			return nil, svcerr.Wrap(svcerr.InvalidArgument("user " + u + " is not a member of the organization"))
		default:
			return nil, svcerr.Wrap(err)
		}
	}
	if err := s.deps.Store.SetOrgAccountGrants(ctx, orgID, acct.ID, users, userID); err != nil {
		return nil, svcerr.Wrap(err)
	}
	if len(users) > 0 {
		s.wakeOrgChats(ctx, orgID, "an org account was granted to members")
	}
	out, err := s.one(ctx, orgID, acct)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.SetOrgProviderAccountGrantsResponse{Account: out}), nil
}
