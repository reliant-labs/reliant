package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// ListArchivedWorktrees pages through the project's archived workspaces, most
// recently archived first. ListWorktrees never returns them.
func (s *Service) ListArchivedWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.ListArchivedWorktreesRequest],
) (*connect.Response[reliantv1.ListArchivedWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	pageSize := defaultListWorktreesPageSize
	if req.Msg.Limit > 0 {
		pageSize = int(req.Msg.Limit)
	}
	offset, err := decodeWorktreePageToken(req.Msg.GetPageToken())
	if err != nil {
		return nil, svcerr.InvalidArgument("invalid page_token")
	}

	projectID := req.Msg.ProjectId
	worktrees, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID:    &projectID,
		ArchivedOnly: true,
		// One extra row tells us whether another page follows.
		Limit:  pageSize + 1,
		Offset: offset,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list archived worktrees"))
	}

	nextPageToken := ""
	if len(worktrees) > pageSize {
		worktrees = worktrees[:pageSize]
		nextPageToken = encodeWorktreePageToken(offset + pageSize)
	}

	protoWorktrees := make([]*reliantv1.Worktree, len(worktrees))
	for i, w := range worktrees {
		protoWorktrees[i] = worktreeToProto(w)
	}
	return connect.NewResponse(&reliantv1.ListArchivedWorktreesResponse{
		Worktrees:     protoWorktrees,
		Total:         int32(len(protoWorktrees)),
		NextPageToken: nextPageToken,
	}), nil
}
