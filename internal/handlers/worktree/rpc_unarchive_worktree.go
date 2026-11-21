package worktree

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// UnarchiveWorktree restores an archived worktree
func (s *Service) UnarchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UnarchiveWorktreeRequest],
) (*connect.Response[reliantv1.UnarchiveWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.deps.Repo.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	if worktree.DeletedAt == nil {
		return nil, svcerr.FailedPrecondition("worktree is not archived")
	}

	// A directory that is already gone cannot be unarchived into a working
	// workspace: the user restores it with RecreateWorktree, which rebuilds it.
	if worktree.CleanupMetadata != nil && worktree.CleanupMetadata.DirectoryDeleted {
		return nil, svcerr.FailedPrecondition("this workspace's files were removed; restore it with recreate")
	}

	// Refuse a name collision before the machine is touched: re-locking the
	// checkouts for a row that then cannot be restored would leave them locked
	// as live under an archived row.
	if holder, err := s.deps.Repo.GetLiveWorktreeByName(ctx, worktree.ProjectID, worktree.Name); err == nil && holder != nil {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
	}

	// Take the archive fence off the directory FIRST, and retire it: the machine
	// re-locks the checkouts as "restored <fence>", so a removal already in
	// flight for this archive finds a lock it does not own and refuses, and no
	// claim built before now can use that fence again. See relockActive.
	proceed, err := s.relockActive(ctx, userID, worktree)
	if err != nil {
		return nil, err
	}
	if !proceed {
		return nil, svcerr.FailedPrecondition("this workspace's files were removed; restore it with recreate")
	}

	if err := s.deps.Repo.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		if errors.Is(err, core.ErrWorktreeNameTaken) {
			return nil, svcerr.FailedPrecondition(fmt.Sprintf(
				"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to unarchive worktree"))
	}

	// Unarchive associated chats
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	msg := "Worktree and associated chats unarchived successfully"
	if m := worktree.CleanupMetadata; m != nil && m.StrippedBytes > 0 {
		msg += "; rebuildable files (node_modules, build output, tool caches) were removed while it was archived, so run its install/generate step before building"
	}
	return connect.NewResponse(&reliantv1.UnarchiveWorktreeResponse{
		Message: msg,
	}), nil
}
