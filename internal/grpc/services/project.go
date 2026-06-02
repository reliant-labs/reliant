// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/mcpconfig"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ProjectService implements the ProjectService RPC handlers
type ProjectService struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	database     db.Repository
	daemonRouter toolexec.DaemonRouter
}

func (s *ProjectService) buildProjectConfigResolver() func(ctx context.Context, projectPath string) (*config.Config, error) {
	if s == nil || s.database == nil {
		return nil
	}
	storedConfigProvider := config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(s.database))
	return func(ctx context.Context, projectPath string) (*config.Config, error) {
		project, err := mcpconfig.ResolveProjectForMCPPath(ctx, s.database, projectPath)
		if err != nil {
			return nil, err
		}
		return storedConfigProvider.GetProjectConfig(ctx, config.ProjectRef{ProjectID: project.ID})
	}
}

// NewProjectService creates a new ProjectService
func NewProjectService(database db.Repository, daemonRouter toolexec.DaemonRouter) *ProjectService {
	return &ProjectService{database: database, daemonRouter: daemonRouter}
}

// sendProjectDaemonCommand sends a command to the user's daemon and unmarshals the response.
// If the daemon is offline, it returns connect.CodeUnavailable.
func (s *ProjectService) sendProjectDaemonCommand(ctx context.Context, userID, commandType string, payload interface{}, resp interface{}) error {
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

// resolveProjectRepoPath resolves a per-RPC repo_id selector against the
// project's repo set and returns the absolute git checkout path. Rules
// match WorktreeService.resolveRepoPath:
//
//   - empty repoID + project has 0 or 1 repos -> use project.Path as-is
//     (legacy single-repo behavior).
//   - empty repoID + project has 2+ repos -> InvalidArgument; multi-repo
//     callers must specify which repo they're acting on.
//   - non-empty repoID -> look up the repo, ensure it belongs to this
//     project, and return <project.Path>/<repo.relative_path>.
//   - repoID not in the project's repo set -> NotFound.
func (s *ProjectService) resolveProjectRepoPath(ctx context.Context, project *db.Project, repoID string) (string, *core.Repo, error) {
	repos, err := s.database.ListReposByProject(ctx, project.ID)
	if err != nil {
		return "", nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}

	if repoID == "" {
		if len(repos) > 1 {
			return "", nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("repo_id required in multi-repo projects"))
		}
		if len(repos) == 1 {
			return filepath.Join(project.Path, repos[0].RelativePath), repos[0], nil
		}
		return project.Path, nil, nil
	}

	for _, r := range repos {
		if r.ID == repoID {
			return filepath.Join(project.Path, r.RelativePath), r, nil
		}
	}
	return "", nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repo not in project"))
}

// projectBelongsToUser checks if a project belongs to a user
func (s *ProjectService) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return nil
}

// projectToProto converts a db.Project to proto Project
func projectToProto(p *db.Project) *reliantv1.Project {
	proto := &reliantv1.Project{
		Id:         p.ID,
		UserId:     p.UserID,
		Name:       p.Name,
		Path:       p.Path,
		IsGitRepo:  p.IsGitRepo,
		IsForge:    p.IsForge,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
		LastActive: p.LastActive.Format(time.RFC3339),
	}
	if p.Description != nil {
		proto.Description = p.Description
	}
	if p.DefaultBranch != nil {
		proto.DefaultBranch = p.DefaultBranch
	}
	if p.RemoteURL != nil {
		proto.RemoteUrl = p.RemoteURL
	}
	return proto
}

// CreateProject creates a new project
func (s *ProjectService) CreateProject(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateProjectRequest],
) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Name == "" || req.Msg.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name and path are required"))
	}

	if req.Msg.Path != "" && !filepath.IsAbs(req.Msg.Path) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project path must be absolute, got: %q", req.Msg.Path))
	}

	// Check if project already exists for this user and path. Path-only
	// lookups are nondeterministic in multi-user setups (the projects table
	// allows the same path under different user_ids), so scope the check.
	if existing, err := s.database.GetProjectByPathAndUser(ctx, req.Msg.Path, userID); err == nil && existing != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a project already exists at this path"))
	}

	// Ensure the project directory exists on the daemon. The cloud
	// workspace pod ships with an empty /home/workspace/projects/, and
	// repo.discover / project.init_files below silently no-op when their
	// target path is missing, leaving the DB row pointing at a phantom
	// directory. MkdirAll is idempotent so this is safe for repeat opens.
	if err := s.sendProjectDaemonCommand(ctx, userID, "fs.mkdir", map[string]string{"path": req.Msg.Path}, nil); err != nil {
		logging.Warn("Failed to mkdir project path via daemon", "error", err, "path", req.Msg.Path)
	}

	// Discover nested git repos under the project path. A project may
	// contain 0..N repos (a docs folder is a valid project with zero).
	// IsGitRepo is derived from "any repos discovered". If the daemon is
	// unreachable, project creation still succeeds with zero repos —
	// repos can be added later via AddRepo.
	type discoveredRepo struct {
		RelativePath string `json:"relative_path"`
		Name         string `json:"name"`
		RemoteURL    string `json:"remote_url,omitempty"`
	}
	var discoverResp struct {
		Discovered []discoveredRepo `json:"discovered"`
		HasForge   bool             `json:"has_forge"`
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "repo.discover", map[string]interface{}{"path": req.Msg.Path}, &discoverResp); err != nil {
		logging.Warn("Failed to discover repos via daemon, project will start with zero repos", "error", err, "path", req.Msg.Path)
	}
	isGitRepo := len(discoverResp.Discovered) > 0

	// Set defaults
	defaultBranch := "main"
	if req.Msg.DefaultBranch != nil && *req.Msg.DefaultBranch != "" {
		defaultBranch = *req.Msg.DefaultBranch
	}

	// Derive the project's canonical remote URL from the root repo (the
	// discovered repo at RelativePath == "" or "."). For a single-repo
	// project this is the repo's git remote; multi-repo workspaces fall
	// back to whichever repo sits at the project root, if any. NULL when
	// no root remote is resolvable (non-git or daemon offline).
	var projectRemoteURL *string
	for _, found := range discoverResp.Discovered {
		rel := strings.TrimSpace(found.RelativePath)
		if (rel == "" || rel == ".") && found.RemoteURL != "" {
			rurl := found.RemoteURL
			projectRemoteURL = &rurl
			break
		}
	}

	now := time.Now().UTC()
	project := &db.Project{
		ID:            uuid.New().String(),
		UserID:        userID,
		Name:          req.Msg.Name,
		Path:          req.Msg.Path,
		Description:   req.Msg.Description,
		IsGitRepo:     isGitRepo,
		DefaultBranch: &defaultBranch,
		RemoteURL:     projectRemoteURL,
		IsForge:       discoverResp.HasForge,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastActive:    now,
	}

	if err := s.database.CreateProject(ctx, project); err != nil {
		// Race-window safety: if a concurrent request inserted the same
		// (user_id, path) pair after our check above, the DB rejects with a
		// UNIQUE violation. Surface that as AlreadyExists so the client can
		// open the existing project instead of seeing an opaque Internal.
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "duplicate key") {
			logging.Info("Project already exists at path (unique constraint)", "userID", userID, "path", req.Msg.Path)
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a project already exists at this path"))
		}
		logging.Error("Failed to create project", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create project"))
	}

	// Persist discovered repos. Failures here are logged but don't fail
	// project creation — the project is still usable, repos can be added
	// later, and re-running discovery will pick them up.
	for _, found := range discoverResp.Discovered {
		repoName := found.Name
		if repoName == "" {
			repoName = filepath.Base(found.RelativePath)
			if repoName == "" || repoName == "." {
				repoName = req.Msg.Name
			}
		}
		var remoteURL *string
		if found.RemoteURL != "" {
			rurl := found.RemoteURL
			remoteURL = &rurl
		}
		repoRecord := &core.Repo{
			ID:           uuid.New().String(),
			ProjectID:    project.ID,
			Name:         repoName,
			RelativePath: found.RelativePath,
			RemoteURL:    remoteURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.database.CreateRepo(ctx, repoRecord); err != nil {
			logging.Warn("Failed to persist discovered repo", "error", err, "project_id", project.ID, "relative_path", found.RelativePath)
		}
	}

	// Create reliant.md with default instructions if it doesn't exist (via daemon)
	var initFilesResp struct {
		CreatedReliantMD  bool   `json:"created_reliant_md"`
		CreatedReliantDir bool   `json:"created_reliant_dir"`
		Error             string `json:"error,omitempty"`
	}
	initPayload := map[string]string{
		"path": req.Msg.Path,
		"default_content": `# Project Instructions

<!-- These instructions are loaded into every agent conversation in this project. -->
<!-- Edit this file to customize how agents work with your codebase. -->

## Guidelines

- 

## Key Context

- 

## Available Skills

Use ` + "`skill list`" + ` to see all available skills. Key skills:
- ` + "`reliant-config`" + `: Configure Reliant (memory, skills, MCP, presets, creating skills)
- ` + "`workflow-builder`" + `: Build and test Reliant workflows
`,
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.init_files", initPayload, &initFilesResp); err != nil {
		logging.Warn("Failed to initialize project files via daemon", "error", err, "path", req.Msg.Path)
	} else if initFilesResp.Error != "" {
		logging.Warn("Failed to initialize project files", "error", initFilesResp.Error, "path", req.Msg.Path)
	}

	// In multi-repo projects, also init .reliant/ in each nested repo so
	// downstream config loaders find the standard layout. Repo-level
	// reliant.md is intentionally not created (opt-in via skip_reliant_md).
	// The repo at RelativePath == "" shares the project-root .reliant/.
	for _, found := range discoverResp.Discovered {
		rel := strings.TrimSpace(found.RelativePath)
		if rel == "" || rel == "." {
			continue
		}
		repoPath := filepath.Join(req.Msg.Path, rel)
		var repoInitResp struct {
			Error string `json:"error,omitempty"`
		}
		repoInitPayload := map[string]interface{}{
			"path":            repoPath,
			"skip_reliant_md": true,
		}
		if err := s.sendProjectDaemonCommand(ctx, userID, "project.init_files", repoInitPayload, &repoInitResp); err != nil {
			logging.Warn("Failed to initialize repo files via daemon", "error", err, "path", repoPath)
		} else if repoInitResp.Error != "" {
			logging.Warn("Failed to initialize repo files", "error", repoInitResp.Error, "path", repoPath)
		}
	}

	// Create main worktree for this project
	mainWorktree := &db.Worktree{
		ID:         uuid.New().String(),
		Name:       defaultBranch,
		Path:       req.Msg.Path,
		Branch:     defaultBranch,
		BaseBranch: defaultBranch,
		ProjectID:  project.ID,
		ChatID:     nil,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
		DeletedAt:  nil,
	}

	if err := s.database.CreateWorktree(ctx, mainWorktree); err != nil {
		logging.Error("Failed to create main worktree", "error", err, "project_id", project.ID)
		// Don't fail the entire project creation if main worktree fails
	}

	// Seed an empty project_configs row so workflows can start immediately
	// without waiting for the daemon to sync. The daemon will overwrite this
	// with real config data via ON CONFLICT ... DO UPDATE when it syncs.
	seedRecord := &db.ProjectConfigRecord{
		ProjectID: project.ID,
		DaemonID:  "seed",
	}
	if err := s.database.UpsertProjectConfigRecord(ctx, seedRecord); err != nil {
		logging.Error("Failed to seed project config record", "error", err, "project_id", project.ID)
	}

	// Tell the daemon to load real configs for this project path.
	// This is fire-and-forget — the seed row above ensures the workflow
	// can start even if the daemon hasn't responded yet.
	if s.daemonRouter != nil {
		if err := s.daemonRouter.SendLoadProjectConfigs(ctx, userID, req.Msg.Path, uuid.New().String()); err != nil {
			logging.Warn("Failed to request daemon config load for new project",
				"error", err,
				"project_id", project.ID,
				"path", req.Msg.Path,
			)
		}
		// Set up a watcher so config changes are pushed continuously.
		// Without this, projects created after daemon startup never receive
		// config snapshots — only the one-shot load above fires (and may
		// silently fail if the daemon hasn't indexed the path yet).
		if err := s.daemonRouter.SendWatchProjectConfigs(ctx, userID, req.Msg.Path, true); err != nil {
			logging.Warn("Failed to request daemon config watch for new project",
				"error", err,
				"project_id", project.ID,
				"path", req.Msg.Path,
			)
		}
	}

	return connect.NewResponse(&reliantv1.CreateProjectResponse{
		Project: projectToProto(project),
	}), nil
}

// ListProjects lists all projects for the current user
func (s *ProjectService) ListProjects(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectsRequest],
) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 100
	}
	offset := int(req.Msg.Offset)
	if offset < 0 {
		offset = 0
	}

	projects, err := s.database.ListProjects(ctx, db.ProjectFilters{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		logging.Error("Failed to list projects", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list projects"))
	}

	protoProjects := make([]*reliantv1.Project, len(projects))
	for i, p := range projects {
		protoProjects[i] = projectToProto(p)
	}

	return connect.NewResponse(&reliantv1.ListProjectsResponse{
		Projects: protoProjects,
		Total:    int32(len(projects)),
	}), nil
}

// GetProject retrieves a project by ID
func (s *ProjectService) GetProject(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectRequest],
) (*connect.Response[reliantv1.GetProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Live-check git status via daemon so we never show a stale modal.
	// Once IsGitRepo is true it's monotonic (a repo stays a repo), so skip the
	// daemon round-trip in that case to avoid ~500ms of latency.
	if !project.IsGitRepo {
		var checkGitResp struct {
			IsGitRepo bool `json:"is_git_repo"`
		}
		if err := s.sendProjectDaemonCommand(ctx, userID, "project.check_git", map[string]string{"path": project.Path}, &checkGitResp); err != nil {
			logging.Warn("GetProject: failed to live-check git status, using DB value", "error", err, "projectID", project.ID)
		} else if checkGitResp.IsGitRepo != project.IsGitRepo {
			project.IsGitRepo = checkGitResp.IsGitRepo
			project.UpdatedAt = time.Now().UTC()
			if updateErr := s.database.UpdateProject(ctx, project, userID); updateErr != nil {
				logging.Warn("GetProject: failed to persist updated git status", "error", updateErr, "projectID", project.ID)
			}
		}
	}

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	analyticsClient.TrackProjectOpened(analytics.ProjectOpenedMetrics{
		ProjectID: project.ID,
		IsGitRepo: project.IsGitRepo,
	})

	return connect.NewResponse(&reliantv1.GetProjectResponse{
		Project: projectToProto(project),
	}), nil
}

// UpdateProject updates a project
func (s *ProjectService) UpdateProject(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectRequest],
) (*connect.Response[reliantv1.UpdateProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Update fields
	if req.Msg.Name != nil {
		project.Name = *req.Msg.Name
	}
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}
	if req.Msg.DefaultBranch != nil {
		project.DefaultBranch = req.Msg.DefaultBranch
	}

	project.UpdatedAt = time.Now().UTC()
	project.LastActive = time.Now().UTC()

	if err := s.database.UpdateProject(ctx, project, userID); err != nil {
		logging.Error("Failed to update project", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectResponse{
		Project: projectToProto(project),
	}), nil
}

// DeleteProject deletes a project
func (s *ProjectService) DeleteProject(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteProjectRequest],
) (*connect.Response[reliantv1.DeleteProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	if err := s.database.DeleteProject(ctx, req.Msg.ProjectId, userID); err != nil {
		logging.Error("Failed to delete project", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete project"))
	}

	return connect.NewResponse(&reliantv1.DeleteProjectResponse{
		Success: true,
		Message: "Project deleted successfully",
	}), nil
}

// TouchProject updates the project's last_active timestamp
func (s *ProjectService) TouchProject(
	ctx context.Context,
	req *connect.Request[reliantv1.TouchProjectRequest],
) (*connect.Response[reliantv1.TouchProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	if err := s.database.TouchProject(ctx, req.Msg.ProjectId, userID); err != nil {
		logging.Error("Failed to touch project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update project activity"))
	}

	if s.daemonRouter != nil {
		project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			logging.Warn("TouchProject: failed to load project for MCP warmup", "projectID", req.Msg.ProjectId, "error", err)
		} else if project != nil && strings.TrimSpace(project.Path) != "" {
			projectID := project.ID
			projectPath := project.Path
			go func() {
				warmupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()

				// Send mcp.ensure_loaded daemon command to warmup MCP servers on the user's machine
				payload, _ := json.Marshal(map[string]string{"project_path": projectPath})
				if _, err := s.daemonRouter.SendDaemonCommand(warmupCtx, userID, "mcp.ensure_loaded", payload, 90000); err != nil {
					logging.Warn("Project MCP warmup via daemon failed", "projectID", projectID, "error", err)
				}
			}()
		}
	}

	return connect.NewResponse(&reliantv1.TouchProjectResponse{
		Success: true,
		Message: "Project activity updated",
	}), nil
}

// GetProjectMetadata returns detailed project metadata
func (s *ProjectService) GetProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectMetadataRequest],
) (*connect.Response[reliantv1.GetProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	resp := &reliantv1.GetProjectMetadataResponse{
		ProjectId:  project.ID,
		Name:       project.Name,
		Path:       project.Path,
		IsGitRepo:  project.IsGitRepo,
		CreatedAt:  project.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  project.UpdatedAt.Format(time.RFC3339),
		LastActive: project.LastActive.Format(time.RFC3339),
	}
	if project.Description != nil {
		resp.Description = project.Description
	}
	if project.DefaultBranch != nil {
		resp.DefaultBranch = project.DefaultBranch
	}

	return connect.NewResponse(resp), nil
}

// UpdateProjectMetadata updates project metadata
func (s *ProjectService) UpdateProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectMetadataRequest],
) (*connect.Response[reliantv1.UpdateProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Update fields
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}

	if err := s.database.UpdateProject(ctx, project, userID); err != nil {
		logging.Error("Failed to update project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectMetadataResponse{
		Project: projectToProto(project),
	}), nil
}

// GetProjectGitInfo returns git repository information
func (s *ProjectService) GetProjectGitInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectGitInfoRequest],
) (*connect.Response[reliantv1.GetProjectGitInfoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
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
		logging.Error("Failed to get git info via daemon", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get git info"))
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
		RemoteUrl:      gitInfo.RemoteURL,
	}), nil
}

// GetProjectGitBranches lists all git branches for a project
func (s *ProjectService) GetProjectGitBranches(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectGitBranchesRequest],
) (*connect.Response[reliantv1.GetProjectGitBranchesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
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
		logging.Error("Failed to get git branches via daemon", "error", err, "projectID", project.ID)
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

// GetProjectInitStatus returns the initialization status of a project
func (s *ProjectService) GetProjectInitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectInitStatusRequest],
) (*connect.Response[reliantv1.GetProjectInitStatusResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Check initialization status via daemon (filesystem checks)
	var initStatus struct {
		Initialized bool   `json:"initialized"`
		Message     string `json:"message"`
	}
	initPayload := struct {
		Path      string `json:"path"`
		IsGitRepo bool   `json:"is_git_repo"`
	}{Path: project.Path, IsGitRepo: project.IsGitRepo}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.check_init_status", initPayload, &initStatus); err != nil {
		logging.Error("Failed to check init status via daemon", "error", err, "projectID", req.Msg.ProjectId)
		// Fallback: assume initialized since it exists in the database
		initStatus.Initialized = true
		initStatus.Message = "Project is initialized"
	}

	return connect.NewResponse(&reliantv1.GetProjectInitStatusResponse{
		Initialized: initStatus.Initialized,
		ProjectId:   req.Msg.ProjectId,
		Message:     initStatus.Message,
	}), nil
}

// InitializeProject initializes a project (no-op in V2)
func (s *ProjectService) InitializeProject(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeProjectRequest],
) (*connect.Response[reliantv1.InitializeProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Project initialization is a no-op in V2 (projects are initialized on creation)
	return connect.NewResponse(&reliantv1.InitializeProjectResponse{
		ProjectId:   project.ID,
		Status:      "initialized",
		Message:     "Project already initialized",
		Initialized: true,
	}), nil
}

// GetProjectChanges returns recent git changes for a project
func (s *ProjectService) GetProjectChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectChangesRequest],
) (*connect.Response[reliantv1.GetProjectChangesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project has no path configured"))
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
		logging.Error("Failed to get git changes via daemon", "error", err, "projectID", req.Msg.ProjectId, "path", project.Path)
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

// Prompt type for JSON marshaling
type promptData struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Content     string `json:"content"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// GetProjectPrompts retrieves project-scoped prompts
func (s *ProjectService) GetProjectPrompts(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectPromptsRequest],
) (*connect.Response[reliantv1.GetProjectPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	projectID := req.Msg.ProjectId
	setting, err := s.database.GetSetting(ctx, userID, &projectID, "project.prompts")
	if err != nil {
		// Return empty prompts if not found
		return connect.NewResponse(&reliantv1.GetProjectPromptsResponse{
			Prompts: []*reliantv1.Prompt{},
		}), nil
	}

	var prompts []promptData
	if setting.Value != "" {
		if err := json.Unmarshal([]byte(setting.Value), &prompts); err != nil {
			logging.Error("Failed to unmarshal project prompts", "error", err, "projectID", req.Msg.ProjectId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse prompts"))
		}
	}

	protoPrompts := make([]*reliantv1.Prompt, len(prompts))
	for i, p := range prompts {
		protoPrompts[i] = &reliantv1.Prompt{
			Id:          p.ID,
			Name:        p.Name,
			Content:     p.Content,
			Description: p.Description,
			CreatedAt:   p.CreatedAt,
			UpdatedAt:   p.UpdatedAt,
		}
	}

	return connect.NewResponse(&reliantv1.GetProjectPromptsResponse{
		Prompts: protoPrompts,
	}), nil
}

// SaveProjectPrompts saves project-scoped prompts
func (s *ProjectService) SaveProjectPrompts(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveProjectPromptsRequest],
) (*connect.Response[reliantv1.SaveProjectPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Convert proto prompts to data structure
	prompts := make([]promptData, len(req.Msg.Prompts))
	for i, p := range req.Msg.Prompts {
		prompts[i] = promptData{
			ID:          p.Id,
			Name:        p.Name,
			Content:     p.Content,
			Description: p.Description,
			CreatedAt:   p.CreatedAt,
			UpdatedAt:   p.UpdatedAt,
		}
	}

	// Marshal prompts to JSON
	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize prompts"))
	}

	projectID := req.Msg.ProjectId

	// Use upsert pattern: try to get existing, then create or update
	setting, err := s.database.GetSetting(ctx, userID, &projectID, "project.prompts")
	if err != nil {
		// Create new setting
		newSetting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: &projectID,
			Key:       "project.prompts",
			Value:     string(promptsJSON),
			ValueType: "string",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.database.CreateSetting(ctx, newSetting); err != nil {
			logging.Error("Failed to create project prompts", "error", err, "projectID", req.Msg.ProjectId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save prompts"))
		}
	} else {
		// Update existing setting
		setting.Value = string(promptsJSON)
		setting.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateSetting(ctx, setting); err != nil {
			logging.Error("Failed to update project prompts", "error", err, "projectID", req.Msg.ProjectId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save prompts"))
		}
	}

	return connect.NewResponse(&reliantv1.SaveProjectPromptsResponse{
		Message: "Prompts saved successfully",
		Prompts: req.Msg.Prompts,
	}), nil
}

// InitializeGitRepo initializes a git repository for a project
func (s *ProjectService) InitializeGitRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeGitRepoRequest],
) (*connect.Response[reliantv1.InitializeGitRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	logging.Info("Attempting to initialize git repository", "projectID", req.Msg.ProjectId, "path", project.Path, "is_git_repo", project.IsGitRepo)

	// Check if already a git repository
	if project.IsGitRepo {
		logging.Warn("Project is already marked as git repository", "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project is already a git repository"))
	}

	// Route git init through daemon (handles path existence check, .git check, init)
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
		InitialBranch:     req.Msg.InitialBranch,
		GitignorePatterns: req.Msg.GitignorePatterns,
		InitialCommit:     req.Msg.InitialCommit,
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.init_git_repo", initPayload, &initResp); err != nil {
		logging.Error("Failed to initialize git repository via daemon", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to initialize git repository: %v", err))
	}
	if !initResp.Success {
		logging.Error("Daemon reported git init failure", "error", initResp.Error, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", initResp.Error))
	}

	// Update project to mark it as a git repository
	project.IsGitRepo = true
	actualBranch := req.Msg.InitialBranch
	if actualBranch == "" {
		actualBranch = "main"
	}
	project.DefaultBranch = &actualBranch

	if err := s.database.UpdateProject(ctx, project, userID); err != nil {
		logging.Error("Failed to update project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update project"))
	}

	// Update the main worktree's branch to match the initialized branch
	// This is necessary because the main worktree was created with a default branch name
	// when the project was first created (before git init)
	worktrees, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &req.Msg.ProjectId,
	})
	if err != nil {
		logging.Warn("Failed to list worktrees for branch update", "error", err, "projectID", req.Msg.ProjectId)
	} else {
		for _, wt := range worktrees {
			if wt.IsMain {
				wt.Branch = actualBranch
				wt.BaseBranch = actualBranch
				wt.Name = actualBranch // Update name to match branch
				wt.UpdatedAt = time.Now().UTC()
				if err := s.database.UpdateWorktree(ctx, wt); err != nil {
					logging.Warn("Failed to update main worktree branch", "error", err, "worktreeID", wt.ID)
				} else {
					logging.Info("Updated main worktree branch", "worktreeID", wt.ID, "branch", actualBranch)
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

// projectDaemonToProto converts a db.ProjectDaemon to its proto form.
func projectDaemonToProto(pd *db.ProjectDaemon) *reliantv1.ProjectDaemon {
	out := &reliantv1.ProjectDaemon{
		ProjectId: pd.ProjectID,
		DaemonId:  pd.DaemonID,
		Path:      pd.Path,
		ClonedAt:  pd.ClonedAt.Format(time.RFC3339),
	}
	if pd.DefaultBranch != nil {
		out.DefaultBranch = pd.DefaultBranch
	}
	return out
}

// ListProjectDaemonsForDaemon returns the project_daemons rows installed on
// the given daemon, filtered to projects owned by the calling user.
func (s *ProjectService) ListProjectDaemonsForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectDaemonsForDaemonRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.DaemonId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	rows, err := s.database.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		logging.Error("Failed to list project_daemons for daemon", "error", err, "daemon_id", req.Msg.DaemonId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list project_daemons"))
	}

	// Filter by ownership: a project_daemons row is only visible to a user
	// if the underlying project belongs to them.
	out := make([]*reliantv1.ProjectDaemon, 0, len(rows))
	for _, pd := range rows {
		if _, err := s.database.GetProjectWithUserCheck(ctx, pd.ProjectID, userID); err != nil {
			continue
		}
		out = append(out, projectDaemonToProto(pd))
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsForDaemonResponse{
		ProjectDaemons: out,
	}), nil
}

// ListProjectDaemons returns every project_daemons row across all the
// caller's projects. Implemented as a fan-out over the user's projects (we
// already have ListProjectDaemonsForProject) rather than a new join query —
// the fan-out is bounded by project count (small) and keeps the SQL surface
// minimal. The picker uses this to tell users *which* daemon hosts a project
// instead of just "another one".
func (s *ProjectService) ListProjectDaemons(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListProjectDaemonsRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	projects, err := s.database.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	if err != nil {
		logging.Error("Failed to list projects for ListProjectDaemons", "error", err, "user_id", userID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list project_daemons"))
	}

	out := make([]*reliantv1.ProjectDaemon, 0)
	for _, p := range projects {
		rows, err := s.database.ListProjectDaemonsForProject(ctx, p.ID)
		if err != nil {
			logging.Error("Failed to list project_daemons for project",
				"error", err, "project_id", p.ID, "user_id", userID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list project_daemons"))
		}
		for _, pd := range rows {
			out = append(out, projectDaemonToProto(pd))
		}
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsResponse{
		ProjectDaemons: out,
	}), nil
}

// MarkProjectInstalled records that a project has a clone on a daemon. The
// project must belong to the calling user; the daemon_id is trusted because
// the user-facing flow that calls this just successfully cloned through the
// gateway, which already authenticated the daemon binding.
func (s *ProjectService) MarkProjectInstalled(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkProjectInstalledRequest],
) (*connect.Response[reliantv1.MarkProjectInstalledResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" || req.Msg.DaemonId == "" || req.Msg.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id, daemon_id, and path are required"))
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	var defaultBranch *string
	if req.Msg.DefaultBranch != nil && *req.Msg.DefaultBranch != "" {
		db := *req.Msg.DefaultBranch
		defaultBranch = &db
	}

	if err := s.database.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, req.Msg.Path, defaultBranch); err != nil {
		logging.Error("Failed to upsert project_daemons row",
			"error", err,
			"project_id", req.Msg.ProjectId,
			"daemon_id", req.Msg.DaemonId,
		)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record project installation"))
	}

	// Return the resolved row so the client can populate UI state without
	// a follow-up list call.
	pd := &db.ProjectDaemon{
		ProjectID:     req.Msg.ProjectId,
		DaemonID:      req.Msg.DaemonId,
		Path:          req.Msg.Path,
		DefaultBranch: defaultBranch,
		ClonedAt:      time.Now().UTC(),
	}
	return connect.NewResponse(&reliantv1.MarkProjectInstalledResponse{
		ProjectDaemon: projectDaemonToProto(pd),
	}), nil
}

// ListRepositoriesForDaemon returns the projects cloned on the given daemon,
// joined with project metadata (name + remote_url) so admin callers can
// render a table without per-row GetProject calls. Filtered to projects
// owned by the calling user.
func (s *ProjectService) ListRepositoriesForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRepositoriesForDaemonRequest],
) (*connect.Response[reliantv1.ListRepositoriesForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.DaemonId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}
	rows, err := s.database.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		logging.Error("ListRepositoriesForDaemon: failed to list project_daemons", "error", err, "daemon_id", req.Msg.DaemonId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repositories"))
	}
	out := make([]*reliantv1.DaemonRepository, 0, len(rows))
	for _, pd := range rows {
		project, err := s.database.GetProjectWithUserCheck(ctx, pd.ProjectID, userID)
		if err != nil {
			// Either the project belongs to a different user or it was
			// hard-deleted while still leaving the FK-cascaded row. Skip
			// quietly so the admin table doesn't show orphans.
			continue
		}
		branch := ""
		if pd.DefaultBranch != nil {
			branch = *pd.DefaultBranch
		} else if project.DefaultBranch != nil {
			branch = *project.DefaultBranch
		}
		remoteURL := ""
		if project.RemoteURL != nil {
			remoteURL = *project.RemoteURL
		}
		out = append(out, &reliantv1.DaemonRepository{
			ProjectId: project.ID,
			Name:      project.Name,
			RemoteUrl: remoteURL,
			Branch:    branch,
			Path:      pd.Path,
			ClonedAt:  pd.ClonedAt.Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListRepositoriesForDaemonResponse{
		Repositories: out,
	}), nil
}

// resolveProjectDaemonRow loads the project_daemons row for (project_id,
// daemon_id) and the parent project, enforcing user ownership. Returns
// NotFound when there's no clone of that project on that daemon.
func (s *ProjectService) resolveProjectDaemonRow(ctx context.Context, projectID, daemonID, userID string) (*db.Project, *core.ProjectDaemon, error) {
	if projectID == "" || daemonID == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id and daemon_id are required"))
	}
	project, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}
	rows, err := s.database.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up project_daemons"))
	}
	for _, pd := range rows {
		if pd.DaemonID == daemonID {
			return project, pd, nil
		}
	}
	return nil, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project is not cloned on that daemon"))
}

// PullProjectOnDaemon runs `git pull` for the project's clone on a daemon.
// Reuses the user's default daemon command channel; control-plane callers
// are expected to invoke this with the daemon's owner user identity.
func (s *ProjectService) PullProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.PullProjectOnDaemonRequest],
) (*connect.Response[reliantv1.PullProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	_, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	}
	var pullResp struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	payload := map[string]string{"path": pd.Path, "branch": branch}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.pull", payload, &pullResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("git pull failed: %w", err))
	}
	// Refresh cloned_at so the admin UI shows "just now" after a successful
	// pull. Errors here are non-fatal — the pull already landed.
	if err := s.database.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.Warn("PullProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.PullProjectOnDaemonResponse{
		Output: pullResp.Output,
	}), nil
}

// RemoveProjectFromDaemon deletes the on-disk clone AND the
// project_daemons row. The Project row itself is untouched (it may still be
// cloned on other daemons). Idempotent — calling on an absent clone still
// succeeds so the UI can clean up half-broken state.
func (s *ProjectService) RemoveProjectFromDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RemoveProjectFromDaemonRequest],
) (*connect.Response[reliantv1.RemoveProjectFromDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	_, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		// NotFound is treated as success — the row's already gone and
		// there's nothing on disk to delete from our perspective. Anything
		// else is a real error.
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
		}
		return nil, err
	}
	var removeResp struct {
		Success bool   `json:"success"`
		Removed bool   `json:"removed"`
		Error   string `json:"error"`
	}
	payload := map[string]string{"path": pd.Path}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.remove", payload, &removeResp); err != nil {
		// On-disk remove failure: still try to drop the DB row so the UI
		// stops listing a ghost. Log so an operator can investigate.
		logging.Warn("RemoveProjectFromDaemon: on-disk remove failed (will still drop project_daemons row)", "error", err, "path", pd.Path)
	}
	if err := s.database.DeleteProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete project_daemons row: %w", err))
	}
	return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
}

// RecloneProjectOnDaemon blows away the existing clone and re-clones from the
// project's remote_url at the recorded default branch. Refuses to run when
// the project has no remote_url (non-git project — nothing to clone from).
func (s *ProjectService) RecloneProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RecloneProjectOnDaemonRequest],
) (*connect.Response[reliantv1.RecloneProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	if project.RemoteURL == nil || *project.RemoteURL == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project has no remote_url; cannot reclone"))
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	} else if project.DefaultBranch != nil {
		branch = *project.DefaultBranch
	}
	var recloneResp struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
		Error   string `json:"error"`
	}
	payload := map[string]string{
		"path":   pd.Path,
		"repo":   *project.RemoteURL,
		"branch": branch,
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.reclone", payload, &recloneResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("git reclone failed: %w", err))
	}
	// Refresh cloned_at — the row's path/branch are unchanged but the
	// "Last cloned/pulled" column should now show "just now".
	if err := s.database.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.Warn("RecloneProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.RecloneProjectOnDaemonResponse{
		Path: recloneResp.Path,
	}), nil
}
