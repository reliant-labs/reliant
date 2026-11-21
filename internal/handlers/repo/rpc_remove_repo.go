package repo

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// =============================================================================
// RemoveRepo: deletes a repo association. Does NOT delete files on disk.
//
// Worktrees attached to the repo cascade-delete via FK; callers that want to
// preserve them must move them to another repo first.
// =============================================================================

func (s *Service) RemoveRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.RemoveRepoRequest],
) (*connect.Response[reliantv1.RemoveRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	repo, _, err := s.authorizeRepo(ctx, req.Msg.RepoId, userID)
	if err != nil {
		return nil, err
	}

	if err := s.deps.Repo.DeleteRepo(ctx, repo.ID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete repo"))
	}

	// NOTE: removing a repo association deliberately does NOT touch
	// is_git_repo. That flag caches whether a .git exists on disk (a daemon
	// observation), not whether the project has registered worktree repos —
	// unregistering the last repo doesn't delete .git from the filesystem, and
	// a plain git project the user never registered still is a git repo. The
	// flag is reconciled from the daemon in GetProject / project discovery.

	return connect.NewResponse(&reliantv1.RemoveRepoResponse{Success: true}), nil
}
