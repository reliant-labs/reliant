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

// ListProjectOrgMoves lists moves newest first. With an org_id it is that
// org's ledger, the moves out of it and into it, for its admins and owners
// (authz.RequireOrgAdmin, a person, asked fresh); without one, the caller's
// own moves.
func (s *Service) ListProjectOrgMoves(
	ctx context.Context,
	req *connect.Request[pb.ListProjectOrgMovesRequest],
) (*connect.Response[pb.ListProjectOrgMovesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	limit, offset := int(req.Msg.GetLimit()), int(req.Msg.GetOffset())
	f := core.ProjectOrgMoveFilters{UserID: userID, Limit: limit, Offset: offset}
	if org := req.Msg.GetOrgId(); org != "" {
		if err := authz.RequireOrgAdmin(ctx, userID, org); err != nil {
			return nil, orgLedgerRefusal(ctx, err)
		}
		f = core.ProjectOrgMoveFilters{OrgID: org, Limit: limit, Offset: offset}
	}
	moves, err := s.deps.Repo.ListProjectOrgMoves(ctx, f)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project moves"))
	}
	// Each row is decided as GetProjectOrgMove decides it, so the list shows
	// exactly what the single read would.
	moves = authz.Visible(ctx, userID, moves, canReadMove)
	out := &pb.ListProjectOrgMovesResponse{Moves: make([]*pb.ProjectOrgMove, 0, len(moves))}
	for _, m := range moves {
		out.Moves = append(out.Moves, projectOrgMoveProto(m))
	}
	return connect.NewResponse(out), nil
}

func orgLedgerRefusal(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.WithCause(svcerr.Unavailable("could not verify organization membership; try again"), err)
	case errors.Is(err, authz.ErrPersonRequired):
		if refusal := auth.RequirePerson(ctx, "read an organization's project moves"); refusal != nil {
			return refusal
		}
		return svcerr.WithReason(svcerr.PermissionDenied("only a person signed in to the Reliant app can read an organization's project moves"), auth.ReasonPersonRequired)
	default:
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only an admin or owner of the organization can read its project moves"), ReasonSourceAdminRequired)
	}
}
