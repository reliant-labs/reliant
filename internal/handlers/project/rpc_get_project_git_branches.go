package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetProjectGitBranches lists all git branches for a project
func (s *Service) GetProjectGitBranches(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectGitBranchesRequest],
) (*connect.Response[reliantv1.GetProjectGitBranchesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	if !project.IsGitRepo {
		return connect.NewResponse(&reliantv1.GetProjectGitBranchesResponse{
			Branches: []*reliantv1.GitBranch{},
		}), nil
	}

	repoRel, _, err := s.resolveProjectRepo(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}
	// A branch picker with nothing to read lists no branches, as it does
	// for a daemon that cannot answer.
	target, err := s.checkout(ctx, userID, project, "see its git status")
	if err != nil {
		logging.InfoContext(ctx, "No checkout to list git branches from", "error", err, "projectID", project.ID)
		return connect.NewResponse(&reliantv1.GetProjectGitBranchesResponse{
			Branches: []*reliantv1.GitBranch{},
		}), nil
	}
	repoPath := target.Join(repoRel)

	// Route branch listing through daemon
	type daemonBranch struct {
		Name          string `json:"name"`
		IsCurrent     bool   `json:"is_current"`
		IsRemote      bool   `json:"is_remote"`
		IsDetached    bool   `json:"is_detached"`
		CommitSHA     string `json:"commit_sha,omitempty"`
		Upstream      string `json:"upstream,omitempty"`
		LastCommitAge int64  `json:"last_commit_age"`
	}
	var branchResp struct {
		Branches []daemonBranch `json:"branches"`
	}
	if err := s.sendToDaemon(ctx, userID, target.DaemonID, "project.git_branches", map[string]string{"path": repoPath}, &branchResp); err != nil {
		logging.ErrorContext(ctx, "Failed to get git branches via daemon", "error", err, "projectID", project.ID)
		return connect.NewResponse(&reliantv1.GetProjectGitBranchesResponse{
			Branches: []*reliantv1.GitBranch{},
		}), nil
	}

	branches := make([]*reliantv1.GitBranch, len(branchResp.Branches))
	for i, b := range branchResp.Branches {
		branches[i] = &reliantv1.GitBranch{
			Name:          b.Name,
			IsCurrent:     b.IsCurrent,
			IsRemote:      b.IsRemote,
			IsDetached:    b.IsDetached,
			CommitSha:     b.CommitSHA,
			Upstream:      b.Upstream,
			LastCommitAge: b.LastCommitAge,
		}
	}

	return connect.NewResponse(&reliantv1.GetProjectGitBranchesResponse{
		Branches: branches,
	}), nil
}
