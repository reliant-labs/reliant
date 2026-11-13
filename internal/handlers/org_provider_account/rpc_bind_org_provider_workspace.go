package org_provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
)

// BindOrgProviderWorkspace binds the workspace of one of the caller's OWN team-plan sign-ins to
// the org (org-provider-accounts.md §3.4). The workspace id is the provider-issued identity that
// account was connected with, never a typed value, so binding proves the admin is in that
// workspace. Matching members' seats to it is routing's (the binding matcher).
func (s *Service) BindOrgProviderWorkspace(
	ctx context.Context,
	req *connect.Request[pb.BindOrgProviderWorkspaceRequest],
) (*connect.Response[pb.BindOrgProviderWorkspaceResponse], error) {
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
	if err := s.gated(); err != nil {
		return nil, err
	}
	// Only the caller's own accounts: another person's id is NotFound, as everywhere.
	mine, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	var seat *core.ProviderAccount
	for i := range mine {
		if mine[i].ID == req.Msg.GetAccountId() {
			seat = &mine[i]
			break
		}
	}
	if seat == nil {
		return nil, svcerr.Wrap(core.ErrProviderAccountNotFound)
	}
	if seat.AuthKind != core.AuthKindOAuth || !core.TeamPlan(seat.Provider, seat.Plan) {
		return nil, svcerr.Wrap(svcerr.FailedPrecondition(
			"bind a workspace from your own sign-in on a team plan: Claude Team or Enterprise, or ChatGPT Team, Business or Enterprise"))
	}
	external := core.ProviderWorkspaceID(*seat)
	if external == "" {
		return nil, svcerr.Wrap(svcerr.FailedPrecondition(
			"this sign-in does not name its workspace; sign in to it again, then bind it"))
	}
	name := seat.OrganizationName
	if name == "" {
		name = accountlimits.PlanDisplay(seat.Plan) + " workspace"
	}
	w, created, err := s.deps.Store.BindOrgProviderWorkspace(ctx, core.OrgProviderWorkspace{
		OrgID: orgID, Provider: seat.Provider, ExternalOrgID: external, DisplayName: name, BoundByUserID: userID,
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if !created {
		activity.Skip(ctx) // already bound to this org
	}
	s.deps.Logger.InfoContext(ctx, "org provider workspace bound",
		"org_id", orgID, "provider", w.Provider, "created", created, "user_id", userID)
	return connect.NewResponse(&pb.BindOrgProviderWorkspaceResponse{Workspace: workspaceProto(w), Created: created}), nil
}
