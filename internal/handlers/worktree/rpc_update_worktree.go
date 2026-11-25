package worktree

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// UpdateWorktree updates a worktree
func (s *Service) UpdateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorktreeRequest],
) (*connect.Response[reliantv1.UpdateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.getWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	if req.Msg.Name != nil {
		worktree.Name = *req.Msg.Name
	}
	if req.Msg.Status != nil {
		worktree.Status = int32(*req.Msg.Status)
	}
	if req.Msg.BaseBranch != nil {
		worktree.BaseBranch = *req.Msg.BaseBranch
	}
	now := time.Now().UTC()
	worktree.UpdatedAt = now
	worktree.LastActive = now

	if err := s.deps.Repo.UpdateWorktree(ctx, worktree); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update worktree"))
	}

	return connect.NewResponse(&reliantv1.UpdateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}
