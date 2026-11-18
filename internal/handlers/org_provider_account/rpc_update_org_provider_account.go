package org_provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// UpdateOrgProviderAccount renames, pauses, resumes or changes the access of an org account.
// Never gated: pausing is how an admin stops the org's spend.
func (s *Service) UpdateOrgProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.UpdateOrgProviderAccountRequest],
) (*connect.Response[pb.UpdateOrgProviderAccountResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetAccountId() == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	patch := core.AccountPatch{Paused: req.Msg.Paused}
	if req.Msg.Label != nil {
		name, err := label(req.Msg.GetLabel())
		if err != nil {
			return nil, err
		}
		patch.Label = &name
	}
	var access string
	if req.Msg.Access != nil {
		var ok bool
		if access, ok = accessToCore(req.Msg.GetAccess()); !ok {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("unknown access"))
		}
	}
	if _, err := admin(ctx, orgID); err != nil {
		return nil, err
	}
	acct, err := s.find(ctx, orgID, req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	if patch.Label != nil || patch.Paused != nil {
		if acct, err = s.deps.Store.PatchOwnedProviderAccount(ctx, core.OrgAccountOwner(orgID), acct.ID, patch); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	if access != "" {
		if acct, err = s.deps.Store.SetOrgAccountAccess(ctx, orgID, acct.ID, access); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	if (patch.Paused != nil && !*patch.Paused) || access != "" {
		s.wakeOrgChats(ctx, orgID, "an org account was resumed or opened to more members")
	}
	out, err := s.one(ctx, orgID, acct)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UpdateOrgProviderAccountResponse{Account: out}), nil
}
