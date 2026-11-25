package worktree

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
)

// RevertFiles reverts/discards file changes via daemon
func (s *Service) RevertFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.RevertFilesRequest],
) (*connect.Response[reliantv1.RevertFilesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if len(req.Msg.Files) == 0 {
		return nil, svcerr.InvalidArgument("at least one file is required")
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

	// Route revert through daemon
	type revertResult struct {
		File    string `json:"file"`
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	var revertResp struct {
		Results []revertResult `json:"results"`
		Error   string         `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.revert", map[string]interface{}{
		"worktree_path": repoPath,
		"files":         req.Msg.Files,
	}, &revertResp); err != nil {
		return nil, daemonerr.Error("revert files", err)
	}

	if revertResp.Error != "" {
		// The daemon's own report of what git said.
		return nil, svcerr.Internal(revertResp.Error)
	}

	var revertedFiles []string
	var errors []string
	for _, r := range revertResp.Results {
		if r.Success {
			revertedFiles = append(revertedFiles, r.File)
		} else {
			errors = append(errors, fmt.Sprintf("%s: %s", r.File, r.Error))
		}
	}

	if len(revertedFiles) == 0 && len(errors) > 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("failed to revert requested files: %s", strings.Join(errors, "; ")))
	}

	message := fmt.Sprintf("Reverted %d file(s)", len(revertedFiles))
	if len(errors) > 0 {
		message = fmt.Sprintf("%s, %d error(s): %s", message, len(errors), strings.Join(errors, "; "))
	}

	return connect.NewResponse(&reliantv1.RevertFilesResponse{
		Message: message,
		Files:   revertedFiles,
	}), nil
}
