package org_provider_account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListOrgProviderWorkspaces returns the workspaces bound to the org and the members' seats it
// pays for. An owner or admin only: the seats name members' sign-ins.
func (s *Service) ListOrgProviderWorkspaces(
	ctx context.Context,
	req *connect.Request[pb.ListOrgProviderWorkspacesRequest],
) (*connect.Response[pb.ListOrgProviderWorkspacesResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if _, err := admin(ctx, orgID); err != nil {
		return nil, err
	}
	bound, err := s.deps.Store.ListOrgProviderWorkspaces(ctx, orgID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	seats, err := s.deps.Store.ListOrgSeats(ctx, orgID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	out := &pb.ListOrgProviderWorkspacesResponse{
		Workspaces: make([]*pb.OrgProviderWorkspace, 0, len(bound)),
		Seats:      make([]*pb.OrgProviderSeat, 0, len(seats)),
	}
	for _, w := range bound {
		out.Workspaces = append(out.Workspaces, workspaceProto(w))
	}
	for _, a := range seats {
		out.Seats = append(out.Seats, &pb.OrgProviderSeat{
			AccountId: a.ID, UserId: a.Owner().ID, Provider: a.Provider,
			Email: optional(a.Email), Plan: optional(a.Plan), AttributionSource: a.AttributionSource,
			ExternalOrgId: optional(core.ProviderWorkspaceID(a)),
		})
	}
	return connect.NewResponse(out), nil
}
