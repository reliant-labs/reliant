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
	"github.com/reliant-labs/reliant/internal/logging"
)

// InitializeGitRepo initializes a git repository for a project
func (s *Service) InitializeGitRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeGitRepoRequest],
) (*connect.Response[reliantv1.InitializeGitRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	logging.DebugContext(ctx, "Attempting to initialize git repository", "projectID", req.Msg.ProjectId, "is_git_repo", project.IsGitRepo)

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

	// git init runs on the machine holding the checkout, at its path there.
	target, err := s.checkout(ctx, userID, project, "initialize git in it")
	if err != nil {
		return nil, err
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
		Path:              target.Path,
		InitialBranch:     initialBranch,
		GitignorePatterns: req.Msg.GitignorePatterns,
		InitialCommit:     req.Msg.InitialCommit,
	}
	if err := s.sendToDaemon(ctx, userID, target.DaemonID, "project.init_git_repo", initPayload, &initResp); err != nil {
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
	existingRepos, err := s.deps.Repos.ListReposByProject(ctx, project.ID)
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
