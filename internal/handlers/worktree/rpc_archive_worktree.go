package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ArchiveWorktree archives a worktree (dedicated endpoint)
func (s *Service) ArchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ArchiveWorktreeRequest],
) (*connect.Response[reliantv1.ArchiveWorktreeResponse], error) {
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

	if worktree.IsMain {
		return nil, svcerr.FailedPrecondition("cannot archive the main project worktree")
	}

	if worktree.DeletedAt != nil {
		return nil, svcerr.FailedPrecondition("worktree is already archived; use delete to permanently remove")
	}

	project, err := s.getProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to get project for worktree cleanup", "error", err)
	}

	// The branch is checked out in the directory until the machine removes it,
	// and git refuses to delete a checked-out branch, so deletion is requested
	// here and carried out after the machine reports the directory removed.
	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, req.Msg.DeleteGitBranch)

	// Archive the worktree
	if err := s.deps.Repo.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)
	s.settleArchived(ctx, userID, worktree, s.ownerCheckout(ctx, userID, project, worktree))

	return connect.NewResponse(&reliantv1.ArchiveWorktreeResponse{
		Message: "Worktree and associated chats archived successfully",
	}), nil
}
