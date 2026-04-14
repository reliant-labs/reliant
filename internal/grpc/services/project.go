// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
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

	// Check if project already exists for this user and path
	existingProject, err := s.database.GetProjectByPath(ctx, req.Msg.Path)
	if err == nil && existingProject != nil && existingProject.UserID == userID {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a project already exists at this path"))
	}

	// Auto-detect if path is a git repository via daemon
	var checkGitResp struct {
		IsGitRepo bool `json:"is_git_repo"`
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.check_git", map[string]string{"path": req.Msg.Path}, &checkGitResp); err != nil {
		logging.Warn("Failed to check git status via daemon, defaulting to false", "error", err, "path", req.Msg.Path)
	}
	isGitRepo := checkGitResp.IsGitRepo

	// Set defaults
	defaultBranch := "main"
	if req.Msg.DefaultBranch != nil && *req.Msg.DefaultBranch != "" {
		defaultBranch = *req.Msg.DefaultBranch
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
		CreatedAt:     now,
		UpdatedAt:     now,
		LastActive:    now,
	}

	if err := s.database.CreateProject(ctx, project); err != nil {
		logging.Error("Failed to create project", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create project"))
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
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_info", map[string]string{"path": project.Path}, &gitInfo); err != nil {
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
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_branches", map[string]string{"path": project.Path}, &branchResp); err != nil {
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
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.git_changes", map[string]string{"path": project.Path}, &changesResp); err != nil {
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
