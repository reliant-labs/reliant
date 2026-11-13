package org_provider_account

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UnbindOrgProviderWorkspace removes a binding and, with it, the org's attribution of every seat
// it matched. Never gated: an admin can always stop paying for a workspace.
func (s *Service) UnbindOrgProviderWorkspace(
	ctx context.Context,
	req *connect.Request[pb.UnbindOrgProviderWorkspaceRequest],
) (*connect.Response[pb.UnbindOrgProviderWorkspaceResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	external := strings.TrimSpace(req.Msg.GetExternalOrgId())
	if provider == "" || external == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider and external_org_id are required"))
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Store.UnbindOrgProviderWorkspace(ctx, orgID, provider, external); err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "org provider workspace unbound", "org_id", orgID, "provider", provider, "user_id", userID)
	return connect.NewResponse(&pb.UnbindOrgProviderWorkspaceResponse{}), nil
}
