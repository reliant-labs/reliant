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

// =============================================================================
// Git Read Operations
// =============================================================================

// GetWorktreeChanges gets file changes for a worktree
func (s *Service) GetWorktreeChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeChangesRequest],
) (*connect.Response[reliantv1.GetWorktreeChangesResponse], error) {
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

	// Get changes via daemon
	type fileChangeEntry struct {
		Path     string `json:"path"`
		Status   string `json:"status"`
		IsNew    bool   `json:"is_new"`
		Diff     string `json:"diff"`
		IsBinary bool   `json:"is_binary"`
	}
	var changesResp struct {
		Branch        string            `json:"branch"`
		Files         []fileChangeEntry `json:"files"`
		TotalFiles    int32             `json:"total_files"`
		Ahead         int32             `json:"ahead"`
		Behind        int32             `json:"behind"`
		DefaultBranch string            `json:"default_branch"`
		Error         string            `json:"error,omitempty"`
	}
	if err := s.gitReadOnDisk(ctx, userID, worktree, func() error {
		repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
		if err != nil {
			return err
		}
		if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.git_changes", map[string]string{
			"worktree_path": repoPath,
			"branch":        worktree.Branch,
			"base_branch":   worktree.BaseBranch,
		}, &changesResp, worktreeReadCommandTimeoutMs); err != nil {
			return daemonerr.Error("get worktree changes", err)
		}
		if changesResp.Error != "" {
			// The daemon's own report of what git said.
			return svcerr.Internal(changesResp.Error)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Convert to proto
	files := make([]*reliantv1.WorktreeFileChange, len(changesResp.Files))
	for i, f := range changesResp.Files {
		var status reliantv1.FileChangeStatus
		switch f.Status {
		case "untracked":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_UNTRACKED
		case "staged":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_STAGED
		case "modified":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED
		default:
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED
		}
		files[i] = &reliantv1.WorktreeFileChange{
			Path:     f.Path,
			Status:   status,
			IsNew:    f.IsNew,
			Diff:     f.Diff,
			IsBinary: f.IsBinary,
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreeChangesResponse{
		Branch:        changesResp.Branch,
		Files:         files,
		TotalFiles:    changesResp.TotalFiles,
		Ahead:         changesResp.Ahead,
		Behind:        changesResp.Behind,
		DefaultBranch: changesResp.DefaultBranch,
	}), nil
}
