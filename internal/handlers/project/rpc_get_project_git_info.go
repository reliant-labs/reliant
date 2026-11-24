package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/gitremote"
)

// GetProjectGitInfo returns git repository information
func (s *Service) GetProjectGitInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectGitInfoRequest],
) (*connect.Response[reliantv1.GetProjectGitInfoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	if !project.IsGitRepo {
		return connect.NewResponse(&reliantv1.GetProjectGitInfoResponse{
			ProjectId: project.ID,
			IsGitRepo: false,
			Message:   "Project is not a git repository",
		}), nil
	}

	repoRel, _, err := s.resolveProjectRepo(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}
	target, err := s.checkout(ctx, userID, project, "see its git status")
	if err != nil {
		return nil, err
	}
	repoPath := target.Join(repoRel)

	// Route all git operations through daemon
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
	if err := s.sendToDaemon(ctx, userID, target.DaemonID, "project.git_info", map[string]string{"path": repoPath}, &gitInfo); err != nil {
		return nil, daemonerr.Error("get git info", err)
	}

	return connect.NewResponse(&reliantv1.GetProjectGitInfoResponse{
		ProjectId:      project.ID,
		IsGitRepo:      true,
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
