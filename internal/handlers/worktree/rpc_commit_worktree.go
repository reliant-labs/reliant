package worktree

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
)

// CommitWorktree commits staged changes in a worktree via daemon
func (s *Service) CommitWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.CommitWorktreeRequest],
) (*connect.Response[reliantv1.CommitWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if req.Msg.Message == "" {
		return nil, svcerr.InvalidArgument("commit message is required")
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.getWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Commit via daemon
	output, err := s.commitViaDaemon(ctx, userID, daemonplacement.WorktreeOwner(worktree), repoPath, req.Msg.Message)
	if err != nil {
		// err's text is the client's: git's words, or the machine's state.
		errStr := err.Error()
		if strings.Contains(errStr, "nothing to commit") || strings.Contains(errStr, "nothing added to commit") {
			return nil, svcerr.WithCause(svcerr.FailedPrecondition("no staged changes to commit; stage files first"), err)
		}
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CommitWorktreeResponse{
		Message: "Changes committed successfully",
		Output:  output,
	}), nil
}
