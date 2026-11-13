package project

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetProjectOrgMove reads one move: the project's owner and the person who
// asked for it read their own; an admin or owner of either org reads one in
// or out of it (control-plane docs/design/org-project-move.md §3.4). Anyone
// else gets NotFound.
func (s *Service) GetProjectOrgMove(
	ctx context.Context,
	req *connect.Request[pb.GetProjectOrgMoveRequest],
) (*connect.Response[pb.GetProjectOrgMoveResponse], error) {
	userID := auth.MustGetUserID(ctx)
	move, err := s.deps.Repo.GetProjectOrgMove(ctx, req.Msg.GetMoveId())
	if errors.Is(err, core.ErrProjectOrgMoveNotFound) {
		return nil, svcerr.NotFound("project move")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the move"))
	}
	if !canReadMove(ctx, userID, move) {
		return nil, svcerr.NotFound("project move")
	}
	return connect.NewResponse(&pb.GetProjectOrgMoveResponse{Move: projectOrgMoveProto(move)}), nil
}

// canReadMove: the move's owner or actor while they may still see their own
// rows of one of its orgs (so a credential bound to a third org sees none),
// or an admin or owner of either of its orgs, asked fresh.
func canReadMove(ctx context.Context, userID string, m *core.ProjectOrgMove) bool {
	if authz.CanAccessPersonal(userID, m.OwnerUserID) || authz.CanAccessPersonal(userID, m.ActorUserID) {
		if authz.CanReadOwn(ctx, userID, &m.FromOrgID) || authz.CanReadOwn(ctx, userID, &m.ToOrgID) {
			return true
		}
	}
	for _, orgID := range []string{m.FromOrgID, m.ToOrgID} {
		if err := authz.RequireOrgAdmin(ctx, userID, orgID); err != nil {
			continue
		}
		return true
	}
	return false
}
