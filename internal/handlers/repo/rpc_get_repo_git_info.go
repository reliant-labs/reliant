package repo

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/gitremote"
)

// =============================================================================
// GetRepoGitInfo: returns git status for a single nested repo.
//
// Delegates to the daemon's existing project.git_info handler — that handler
// is already path-parameterized, so we just point it at the repo's absolute path.
// =============================================================================

func (s *Service) GetRepoGitInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetRepoGitInfoRequest],
) (*connect.Response[reliantv1.GetRepoGitInfoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	repo, project, err := s.authorizeRepo(ctx, req.Msg.RepoId, userID)
	if err != nil {
		return nil, err
	}

	target, err := s.checkout(ctx, userID, project, "see its git status")
	if err != nil {
		return nil, err
	}
	repoPath := target.Join(repo.RelativePath)

	var gitInfo struct {
		CurrentBranch  string   `json:"current_branch"`
		RemoteURL      string   `json:"remote_url"`
		HasChanges     bool     `json:"has_changes"`
		Status         string   `json:"status"`
		StagedFiles    []string `json:"staged_files"`
		UnstagedFiles  []string `json:"unstaged_files"`
		UntrackedFiles []string `json:"untracked_files"`
		Ahead          int32    `json:"ahead"`
		Behind         int32    `json:"behind"`
	}
	if err := s.sendToCheckout(ctx, userID, target, "project.git_info", map[string]string{"path": repoPath}, &gitInfo); err != nil {
		return nil, daemonerr.Error("get git info", err)
	}

	return connect.NewResponse(&reliantv1.GetRepoGitInfoResponse{
		RepoId:         repo.ID,
		CurrentBranch:  gitInfo.CurrentBranch,
		HasChanges:     gitInfo.HasChanges,
		Status:         gitInfo.Status,
		StagedFiles:    gitInfo.StagedFiles,
		UnstagedFiles:  gitInfo.UnstagedFiles,
		UntrackedFiles: gitInfo.UntrackedFiles,
		Ahead:          gitInfo.Ahead,
		Behind:         gitInfo.Behind,
		RemoteUrl:      gitremote.StripCredentials(gitInfo.RemoteURL),
	}), nil
}
