package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
)

// GetWorktreeGitStatus gets the overall git status for a worktree
func (s *Service) GetWorktreeGitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeGitStatusRequest],
) (*connect.Response[reliantv1.GetWorktreeGitStatusResponse], error) {
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

	// Get git status via daemon
	var statusResp struct {
		Branch         string   `json:"branch"`
		HasChanges     bool     `json:"has_changes"`
		Status         string   `json:"status"`
		StagedFiles    []string `json:"staged_files"`
		UnstagedFiles  []string `json:"unstaged_files"`
		UntrackedFiles []string `json:"untracked_files"`
		Ahead          int32    `json:"ahead"`
		Behind         int32    `json:"behind"`
	}
	var repoPath string
	if err := s.gitReadOnDisk(ctx, userID, worktree, func() error {
		var err error
		repoPath, _, err = s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
		if err != nil {
			return err
		}
		if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.git_status", map[string]string{
			"worktree_path": repoPath,
			"branch":        worktree.Branch,
		}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
			return daemonerr.Error("get git status", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetWorktreeGitStatusResponse{
		WorktreeId:     worktree.ID,
		Path:           repoPath,
		Branch:         statusResp.Branch,
		Clean:          statusResp.Status == "clean",
		HasChanges:     statusResp.HasChanges,
		StagedFiles:    statusResp.StagedFiles,
		ModifiedFiles:  statusResp.UnstagedFiles,
		UntrackedFiles: statusResp.UntrackedFiles,
		Ahead:          statusResp.Ahead,
		Behind:         statusResp.Behind,
	}), nil
}
