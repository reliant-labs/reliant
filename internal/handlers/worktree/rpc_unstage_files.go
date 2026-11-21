package worktree

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
)

// UnstageFiles unstages files in a worktree
func (s *Service) UnstageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.UnstageFilesRequest],
) (*connect.Response[reliantv1.UnstageFilesResponse], error) {
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

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Unstage files via daemon
	type unstageReq struct {
		WorktreePath string   `json:"worktree_path"`
		Files        []string `json:"files"`
	}
	var unstageResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.unstage", unstageReq{
		WorktreePath: repoPath,
		Files:        req.Msg.Files,
	}, &unstageResp); err != nil {
		return nil, daemonerr.Error("unstage files", err)
	}
	if !unstageResp.Success {
		return nil, svcerr.Internal(fmt.Sprintf("failed to unstage files: %s", unstageResp.Error))
	}

	return connect.NewResponse(&reliantv1.UnstageFilesResponse{
		Message: "Files unstaged successfully",
		Files:   req.Msg.Files,
	}), nil
}
