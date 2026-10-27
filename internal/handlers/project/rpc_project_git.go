package project

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitref"
	"github.com/reliant-labs/reliant/internal/gitremote"
	"github.com/reliant-labs/reliant/internal/logging"
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

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
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

	repoPath, _, err := s.resolveProjectRepoPath(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

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
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_info", map[string]string{"path": repoPath}, &gitInfo); err != nil {
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

	repoPath, _, err := s.resolveProjectRepoPath(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

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
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_branches", map[string]string{"path": repoPath}, &branchResp); err != nil {
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

// GetProjectChanges returns recent git changes for a project
func (s *Service) GetProjectChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectChangesRequest],
) (*connect.Response[reliantv1.GetProjectChangesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Check if project is a git repository
	if !project.IsGitRepo {
		return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
			Branch:     "",
			Files:      []*reliantv1.FileChange{},
			TotalFiles: 0,
		}), nil
	}

	if project.Path == "" {
		return nil, svcerr.InvalidArgument("project has no path configured")
	}

	repoPath, _, err := s.resolveProjectRepoPath(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Route git changes through daemon
	type daemonFileChange struct {
		Path            string `json:"path"`
		Status          string `json:"status"`
		IsNew           bool   `json:"is_new"`
		Diff            string `json:"diff,omitempty"`
		Content         string `json:"content,omitempty"`
		OriginalContent string `json:"original_content,omitempty"`
	}
	var changesResp struct {
		Branch     string             `json:"branch"`
		Files      []daemonFileChange `json:"files"`
		TotalFiles int32              `json:"total_files"`
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_changes", map[string]string{"path": repoPath}, &changesResp); err != nil {
		logging.ErrorContext(ctx, "Failed to get git changes via daemon", "error", err, "projectID", req.Msg.ProjectId, "path", project.Path)
		return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
			Branch:     "",
			Files:      []*reliantv1.FileChange{},
			TotalFiles: 0,
		}), nil
	}

	files := make([]*reliantv1.FileChange, len(changesResp.Files))
	for i, f := range changesResp.Files {
		files[i] = &reliantv1.FileChange{
			Path:            f.Path,
			Status:          fileChangeStatusFromString(f.Status),
			IsNew:           f.IsNew,
			Diff:            f.Diff,
			Content:         f.Content,
			OriginalContent: f.OriginalContent,
		}
	}

	return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
		Branch:     changesResp.Branch,
		Files:      files,
		TotalFiles: changesResp.TotalFiles,
	}), nil
}

// InitializeGitRepo initializes a git repository for a project
func (s *Service) InitializeGitRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeGitRepoRequest],
) (*connect.Response[reliantv1.InitializeGitRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	logging.DebugContext(ctx, "Attempting to initialize git repository", "projectID", req.Msg.ProjectId, "path", project.Path, "is_git_repo", project.IsGitRepo)

	// Check if already a git repository
	if project.IsGitRepo {
		logging.WarnContext(ctx, "Project is already marked as git repository", "projectID", req.Msg.ProjectId)
		return nil, svcerr.FailedPrecondition("project is already a git repository")
	}

	// Normalize + validate the branch name before anything touches disk or
	// the DB, so a bad value (e.g. "main " with a trailing space from the
	// UI) fails as invalid_argument instead of half-initializing the repo.
	initialBranch := gitref.NormalizeBranchName(req.Msg.InitialBranch)
	if err := gitref.ValidateBranchName(initialBranch); err != nil {
		// The validator's sentence names the branch and the rule it broke.
		return nil, svcerr.InvalidArgument(err.Error())
	}

	// Route git init through daemon (handles path existence check, adopting
	// an existing .git left behind by a previously failed init, and init)
	var initResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	initPayload := struct {
		Path              string   `json:"path"`
		InitialBranch     string   `json:"initial_branch"`
		GitignorePatterns []string `json:"gitignore_patterns"`
		InitialCommit     bool     `json:"initial_commit"`
	}{
		Path:              project.Path,
		InitialBranch:     initialBranch,
		GitignorePatterns: req.Msg.GitignorePatterns,
		InitialCommit:     req.Msg.InitialCommit,
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.init_git_repo", initPayload, &initResp); err != nil {
		return nil, daemonerr.Error("initialize git repository", err)
	}
	if !initResp.Success {
		// The daemon's own refusal (a missing path, a .git it cannot adopt),
		// from the user's machine: the actionable part of the message.
		return nil, svcerr.FailedPrecondition(initResp.Error)
	}

	// Update project to mark it as a git repository
	project.IsGitRepo = true
	actualBranch := initialBranch
	project.DefaultBranch = &actualBranch

	if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project"))
	}

	// Register the project root as a Repo row. Worktree creation gates on
	// ListReposByProject (not project.is_git_repo), so without this a
	// freshly initialized project would still fail with "project has no git
	// repos".
	//
	// Skip when the project already has ANY repo row. That covers two cases
	// with one rule: the root is already registered (idempotency), or the
	// project has nested repos, in which case a root row must NOT be added.
	// Adding one would contradict repo.Discover, which deliberately drops the
	// root once nested repos are found, and would make worktree creation fan
	// out an extra checkout for a repo the user never asked for.
	existingRepos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list repos after git init; skipping root repo registration",
			"error", err, "projectID", project.ID)
	} else if len(existingRepos) == 0 {
		now := time.Now().UTC()
		rootRepo := &core.Repo{
			ID:           uuid.New().String(),
			ProjectID:    project.ID,
			Name:         project.Name,
			RelativePath: "",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.deps.Repo.CreateRepo(ctx, rootRepo); err != nil {
			logging.WarnContext(ctx, "Failed to register root repo after git init", "error", err, "projectID", project.ID)
		}
	}

	// Update the main worktree's branch to match the initialized branch
	// This is necessary because the main worktree was created with a default branch name
	// when the project was first created (before git init)
	worktrees, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &req.Msg.ProjectId,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list worktrees for branch update", "error", err, "projectID", req.Msg.ProjectId)
	} else {
		for _, wt := range worktrees {
			if wt.IsMain {
				wt.Branch = actualBranch
				wt.BaseBranch = actualBranch
				wt.Name = actualBranch // Update name to match branch
				wt.UpdatedAt = time.Now().UTC()
				if err := s.deps.Repo.UpdateWorktree(ctx, wt); err != nil {
					logging.WarnContext(ctx, "Failed to update main worktree branch", "error", err, "worktreeID", wt.ID)
				} else {
					logging.InfoContext(ctx, "Updated main worktree branch", "worktreeID", wt.ID, "branch", actualBranch)
				}
				break
			}
		}
	}

	return connect.NewResponse(&reliantv1.InitializeGitRepoResponse{
		Message:       "Git repository initialized successfully",
		ProjectId:     req.Msg.ProjectId,
		IsGitRepo:     true,
		DefaultBranch: *project.DefaultBranch,
	}), nil
}
