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

// =============================================================================
// Git Write Operations
// =============================================================================

// StageFiles stages files in a worktree
func (s *Service) StageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.StageFilesRequest],
) (*connect.Response[reliantv1.StageFilesResponse], error) {
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

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Stage files via daemon
	type stageReq struct {
		WorktreePath string   `json:"worktree_path"`
		Files        []string `json:"files"`
	}
	var stageResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.stage", stageReq{
		WorktreePath: repoPath,
		Files:        req.Msg.Files,
	}, &stageResp); err != nil {
		return nil, daemonerr.Error("stage files", err)
	}
	if !stageResp.Success {
		// A path git has neither on disk nor in the index — the file was
		// deleted after the changes list showed it — is the state of the
		// user's worktree, not a server failure. As Internal it was a browser
		// issue per click (prod 2026-11-05, e39fe2ca: a .png that was gone).
		if strings.Contains(stageResp.Error, "did not match any files") {
			return nil, svcerr.FailedPrecondition(fmt.Sprintf("failed to stage files: %s", stageResp.Error))
		}
		return nil, svcerr.Internal(fmt.Sprintf("failed to stage files: %s", stageResp.Error))
	}

	return connect.NewResponse(&reliantv1.StageFilesResponse{
		Message: "Files staged successfully",
		Files:   req.Msg.Files,
	}), nil
}
