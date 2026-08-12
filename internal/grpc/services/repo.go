// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// RepoService handles RPCs for nested git repositories within a project.
type RepoService struct {
	reliantv1connect.UnimplementedRepoServiceHandler
	database     db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewRepoService creates a new RepoService.
func NewRepoService(database db.Repository, daemonRouter toolexec.DaemonRouter) *RepoService {
	return &RepoService{database: database, daemonRouter: daemonRouter}
}

// sendDaemonCommand forwards a JSON command to the user's daemon.
func (s *RepoService) sendDaemonCommand(ctx context.Context, userID, commandType string, payload, resp interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// authorizeRepo loads a repo and verifies the caller owns its parent project.
func (s *RepoService) authorizeRepo(ctx context.Context, repoID, userID string) (*core.Repo, *db.Project, error) {
	repo, err := s.database.GetRepo(ctx, repoID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repo not found"))
	}
	project, err := s.database.GetProjectWithUserCheck(ctx, repo.ProjectID, userID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repo not found"))
	}
	return repo, project, nil
}

// authorizeProject verifies the caller owns the named project.
func (s *RepoService) authorizeProject(ctx context.Context, projectID, userID string) (*db.Project, error) {
	project, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return project, nil
}

func repoToProto(r *core.Repo) *reliantv1.Repo {
	out := &reliantv1.Repo{
		Id:           r.ID,
		ProjectId:    r.ProjectID,
		Name:         r.Name,
		RelativePath: r.RelativePath,
		CreatedAt:    r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    r.UpdatedAt.Format(time.RFC3339),
	}
	if r.RemoteURL != nil {
		out.RemoteUrl = *r.RemoteURL
	}
	return out
}

// =============================================================================
// DiscoverRepos: scans the project directory for nested git repos via daemon.
// Results are NOT persisted — the caller decides which to add via AddRepo.
// =============================================================================

func (s *RepoService) DiscoverRepos(
	ctx context.Context,
	req *connect.Request[reliantv1.DiscoverReposRequest],
) (*connect.Response[reliantv1.DiscoverReposResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	type discoveredRepo struct {
		RelativePath string `json:"relative_path"`
		Name         string `json:"name"`
		RemoteURL    string `json:"remote_url,omitempty"`
	}
	var daemonResp struct {
		Discovered []discoveredRepo `json:"discovered"`
	}
	payload := map[string]interface{}{
		"path":      project.Path,
		"max_depth": req.Msg.MaxDepth,
	}
	if err := s.sendDaemonCommand(ctx, userID, "repo.discover", payload, &daemonResp); err != nil {
		logging.Error("Failed to discover repos via daemon", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon unreachable; cannot scan filesystem"))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]*reliantv1.Repo, len(daemonResp.Discovered))
	for i, d := range daemonResp.Discovered {
		out[i] = &reliantv1.Repo{
			ProjectId:    project.ID,
			Name:         d.Name,
			RelativePath: d.RelativePath,
			RemoteUrl:    d.RemoteURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	return connect.NewResponse(&reliantv1.DiscoverReposResponse{Discovered: out}), nil
}

// =============================================================================
// ListRepos: returns persisted repos for a project.
// =============================================================================

func (s *RepoService) ListRepos(
	ctx context.Context,
	req *connect.Request[reliantv1.ListReposRequest],
) (*connect.Response[reliantv1.ListReposResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if _, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	repos, err := s.database.ListReposByProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to list repos", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos"))
	}

	out := make([]*reliantv1.Repo, len(repos))
	for i, r := range repos {
		out[i] = repoToProto(r)
	}
	return connect.NewResponse(&reliantv1.ListReposResponse{Repos: out}), nil
}

// =============================================================================
// AddRepo: persists a new nested repo for a project.
//
// Validates the on-disk path exists + has a .git via the daemon. Reads the
// remote URL opportunistically. Rejects duplicates by (project_id, relative_path).
// =============================================================================

func (s *RepoService) AddRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.AddRepoRequest],
) (*connect.Response[reliantv1.AddRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	relPath := req.Msg.RelativePath
	repoPath := filepath.Join(project.Path, relPath)

	// Validate via daemon: confirm path is a git repo. We piggyback on
	// repo.discover with max_depth=0 to scan just the requested path —
	// it returns one Found if the path is a git repo, zero otherwise.
	type discoveredRepo struct {
		RelativePath string `json:"relative_path"`
		RemoteURL    string `json:"remote_url,omitempty"`
	}
	var daemonResp struct {
		Discovered []discoveredRepo `json:"discovered"`
	}
	if err := s.sendDaemonCommand(ctx, userID, "repo.discover", map[string]interface{}{
		"path":      repoPath,
		"max_depth": 1,
	}, &daemonResp); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon unreachable; cannot validate path"))
	}
	var remoteURL *string
	foundGit := false
	for _, d := range daemonResp.Discovered {
		if d.RelativePath == "" || d.RelativePath == "." {
			foundGit = true
			if d.RemoteURL != "" {
				rurl := d.RemoteURL
				remoteURL = &rurl
			}
			break
		}
	}
	if !foundGit {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("path %q is not a git repository", relPath))
	}

	// Reject duplicates.
	if existing, err := s.database.GetRepoByProjectAndPath(ctx, project.ID, relPath); err == nil && existing != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("repo at %q already registered for this project", relPath))
	}

	name := ""
	if req.Msg.Name != nil {
		name = *req.Msg.Name
	}
	if name == "" {
		name = filepath.Base(repoPath)
		if name == "" || name == "." || name == "/" {
			name = project.Name
		}
	}

	now := time.Now().UTC()
	record := &core.Repo{
		ID:           uuid.New().String(),
		ProjectID:    project.ID,
		Name:         name,
		RelativePath: relPath,
		RemoteURL:    remoteURL,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.database.CreateRepo(ctx, record); err != nil {
		logging.Error("Failed to persist repo", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to add repo"))
	}

	// Flip is_git_repo true if it wasn't already — adding the first repo to
	// a previously empty project makes it git-bearing.
	if !project.IsGitRepo {
		project.IsGitRepo = true
		project.UpdatedAt = now
		if err := s.database.UpdateProject(ctx, project, userID); err != nil {
			logging.Warn("Failed to flip project.is_git_repo", "error", err, "projectID", project.ID)
		}
	}

	return connect.NewResponse(&reliantv1.AddRepoResponse{Repo: repoToProto(record)}), nil
}

// =============================================================================
// RemoveRepo: deletes a repo association. Does NOT delete files on disk.
//
// Worktrees attached to the repo cascade-delete via FK; callers that want to
// preserve them must move them to another repo first.
// =============================================================================

func (s *RepoService) RemoveRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.RemoveRepoRequest],
) (*connect.Response[reliantv1.RemoveRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	repo, _, err := s.authorizeRepo(ctx, req.Msg.RepoId, userID)
	if err != nil {
		return nil, err
	}

	if err := s.database.DeleteRepo(ctx, repo.ID); err != nil {
		logging.Error("Failed to delete repo", "error", err, "repoID", repo.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete repo"))
	}

	// NOTE: removing a repo association deliberately does NOT touch
	// is_git_repo. That flag caches whether a .git exists on disk (a daemon
	// observation), not whether the project has registered worktree repos —
	// unregistering the last repo doesn't delete .git from the filesystem, and
	// a plain git project the user never registered still is a git repo. The
	// flag is reconciled from the daemon in GetProject / project discovery.

	return connect.NewResponse(&reliantv1.RemoveRepoResponse{Success: true}), nil
}

// =============================================================================
// GetRepoGitInfo: returns git status for a single nested repo.
//
// Delegates to the daemon's existing project.git_info handler — that handler
// is already path-parameterized, so we just point it at the repo's absolute path.
// =============================================================================

func (s *RepoService) GetRepoGitInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetRepoGitInfoRequest],
) (*connect.Response[reliantv1.GetRepoGitInfoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	repo, project, err := s.authorizeRepo(ctx, req.Msg.RepoId, userID)
	if err != nil {
		return nil, err
	}

	repoPath := filepath.Join(project.Path, repo.RelativePath)

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
	if err := s.sendDaemonCommand(ctx, userID, "project.git_info", map[string]string{"path": repoPath}, &gitInfo); err != nil {
		logging.Error("Failed to get git info via daemon", "error", err, "repoID", repo.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get git info"))
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
		RemoteUrl:      gitInfo.RemoteURL,
	}), nil
}
