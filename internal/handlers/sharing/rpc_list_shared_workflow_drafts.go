package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListSharedWorkflowDrafts lists the saved workflows other members shared
// with an org the caller is a live member of, each decided by the viewer rule
// and in the shared shape (no source path, origin or version).
func (s *Service) ListSharedWorkflowDrafts(
	ctx context.Context,
	req *connect.Request[pb.ListSharedWorkflowDraftsRequest],
) (*connect.Response[pb.ListSharedWorkflowDraftsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := auth.RequirePerson(ctx, "read what others shared with your organization"); err != nil {
		return nil, err
	}
	org, err := s.sharedOrg(ctx, userID, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := &pb.ListSharedWorkflowDraftsResponse{Workflows: []*pb.SharedWorkflowDraft{}}
	if org == "" {
		return connect.NewResponse(out), nil
	}
	limit, offset := page(req.Msg.GetLimit(), req.Msg.GetOffset())
	workflows, err := s.deps.Shares.ListSharedWorkflowDrafts(ctx, core.SharedWorkflowDraftFilters{
		OrgID: org, ViewerUserID: userID, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list shared saved workflows"))
	}
	ctx = authz.WithViewerReads(ctx)
	for _, w := range authz.SharedWorkflowDraftsFor(ctx, userID, workflows) {
		out.Workflows = append(out.Workflows, sharedWorkflowDraft(w, true))
	}
	return connect.NewResponse(out), nil
}
