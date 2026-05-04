// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// WorktreeService implements the WorktreeService RPC handlers
type WorktreeService struct {
	reliantv1connect.UnimplementedWorktreeServiceHandler
	database     db.Repository
	tempClient   client.Client
	daemonRouter toolexec.DaemonRouter
}

// NewWorktreeService creates a new WorktreeService
func NewWorktreeService(database db.Repository, tempClient client.Client, daemonRouter toolexec.DaemonRouter) *WorktreeService {
	return &WorktreeService{database: database, tempClient: tempClient, daemonRouter: daemonRouter}
}

// sendWorktreeDaemonCommand sends a command to the user's daemon and unmarshals the response.
func (s *WorktreeService) sendWorktreeDaemonCommand(ctx context.Context, userID, commandType string, payload interface{}, resp interface{}) error {
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

// =============================================================================
// Permission Helpers
// =============================================================================

// worktreeBelongsToUser checks if a worktree belongs to a user via its project
func (s *WorktreeService) worktreeBelongsToUser(ctx context.Context, worktreeID string, userID string) error {
	worktree, err := s.database.GetWorktree(ctx, worktreeID)
	if err != nil || worktree == nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if _, err := s.database.GetProjectWithUserCheck(ctx, worktree.ProjectID, userID); err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	return nil
}

// projectBelongsToUser checks if a project belongs to a user
func (s *WorktreeService) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	if _, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID); err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	return nil
}

// =============================================================================
// Conversion Helpers
// =============================================================================

// worktreeToProto converts a db.Worktree to proto Worktree
func worktreeToProto(w *db.Worktree) *reliantv1.Worktree {
	proto := &reliantv1.Worktree{
		Id:         w.ID,
		Name:       w.Name,
		Path:       w.Path,
		Branch:     w.Branch,
		BaseBranch: w.BaseBranch,
		ProjectId:  w.ProjectID,
		Status:     worktreeStatusFromInt32(w.Status),
		IsMain:     w.IsMain,
		CreatedAt:  w.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  w.UpdatedAt.Format(time.RFC3339),
		LastActive: w.LastActive.Format(time.RFC3339),
	}

	if w.ChatID != nil {
		proto.ChatId = w.ChatID
	}

	if w.DeletedAt != nil {
		deletedAt := w.DeletedAt.Format(time.RFC3339)
		proto.DeletedAt = &deletedAt
	}

	if w.CleanupMetadata != nil {
		proto.CleanupMetadata = &reliantv1.CleanupMetadata{
			DirectoryDeleted: w.CleanupMetadata.DirectoryDeleted,
			BranchDeleted:    w.CleanupMetadata.BranchDeleted,
		}
	}

	return proto
}

// generateRepoID generates a unique ID for a git repository via daemon
func (s *WorktreeService) generateRepoID(ctx context.Context, userID, projectPath string) (string, error) {
	var resp struct {
		RepoID string `json:"repo_id"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.generate_repo_id", map[string]string{"project_path": projectPath}, &resp); err != nil {
		return "", fmt.Errorf("failed to generate repo ID: %w", err)
	}
	return resp.RepoID, nil
}

// validateWorktreeForGitOps validates that a worktree is suitable for git operations
func (s *WorktreeService) validateWorktreeForGitOps(ctx context.Context, userID string, worktree *db.Worktree) error {
	if worktree.DeletedAt != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree is archived; unarchive it before performing git operations"))
	}

	var resp struct {
		Exists bool   `json:"exists"`
		Error  string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.validate_path", map[string]string{"path": worktree.Path}, &resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot access worktree directory: %v", err))
	}
	if !resp.Exists {
		if resp.Error == "not_found" {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree directory does not exist: %s; the worktree may need to be recreated", worktree.Path))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot access worktree directory: %s", resp.Error))
	}

	return nil
}

// =============================================================================
// CRUD Operations
// =============================================================================

// CreateWorktree creates a new worktree
func (s *WorktreeService) CreateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorktreeRequest],
) (*connect.Response[reliantv1.CreateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	if req.Msg.Branch == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("branch is required"))
	}
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Check project permission
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Get the project
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	if !project.IsGitRepo {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project must be a git repository to create worktrees; please initialize git for this project first"))
	}

	baseBranch := "main"
	if req.Msg.BaseBranch != nil && *req.Msg.BaseBranch != "" {
		baseBranch = *req.Msg.BaseBranch
	}

	// Generate repo ID for centralized worktree location
	repoID, err := s.generateRepoID(ctx, userID, project.Path)
	if err != nil {
		logging.Error("Failed to generate repo ID", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to generate repo ID"))
	}

	// Handle force mode - clean up existing worktree via daemon + database
	if req.Msg.Force {
		if err := s.handleForceCreate(ctx, userID, project.Path, req.Msg.Name, req.Msg.Branch, req.Msg.ProjectId); err != nil {
			return nil, err
		}
	}

	// Determine source path for file copying
	sourcePath := project.Path
	if req.Msg.SourceWorktreeId != nil && *req.Msg.SourceWorktreeId != "" {
		// Verify user owns the source worktree before using it
		if err := s.worktreeBelongsToUser(ctx, *req.Msg.SourceWorktreeId, userID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source worktree not found"))
		}
		sourceWorktree, err := s.database.GetWorktree(ctx, *req.Msg.SourceWorktreeId)
		if err != nil {
			logging.Warn("Source worktree not found, falling back to project path", "sourceWorktreeId", *req.Msg.SourceWorktreeId, "error", err)
		} else {
			sourcePath = sourceWorktree.Path
		}
	}

	// Create the worktree via daemon (handles mkdir, branch check, git worktree add, file copy)
	type createReq struct {
		ProjectPath string   `json:"project_path"`
		RepoID      string   `json:"repo_id"`
		Name        string   `json:"name"`
		Branch      string   `json:"branch"`
		BaseBranch  string   `json:"base_branch"`
		Force       bool     `json:"force"`
		CopyFiles   []string `json:"copy_files,omitempty"`
		SourcePath  string   `json:"source_path,omitempty"`
	}
	var createResp struct {
		Success      bool   `json:"success"`
		WorktreePath string `json:"worktree_path"`
		Error        string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.create", createReq{
		ProjectPath: project.Path,
		RepoID:      repoID,
		Name:        req.Msg.Name,
		Branch:      req.Msg.Branch,
		BaseBranch:  baseBranch,
		Force:       req.Msg.Force,
		CopyFiles:   req.Msg.CopyFiles,
		SourcePath:  sourcePath,
	}, &createResp); err != nil {
		logging.Error("Failed to create git worktree via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create worktree"))
	}
	if !createResp.Success {
		logging.Error("Failed to create git worktree", "error", createResp.Error)
		return nil, s.parseGitWorktreeError(createResp.Error, createResp.WorktreePath, req.Msg.Branch, baseBranch)
	}
	worktreePath := createResp.WorktreePath

	// Create worktree record
	worktreeID := uuid.New().String()
	now := time.Now().UTC()

	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}

	worktree := &db.Worktree{
		ID:         worktreeID,
		Name:       req.Msg.Name,
		Path:       worktreePath,
		Branch:     req.Msg.Branch,
		BaseBranch: baseBranch,
		ProjectID:  req.Msg.ProjectId,
		ChatID:     chatID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}

	if err := s.database.CreateWorktree(ctx, worktree); err != nil {
		logging.Error("Failed to create worktree in database", "error", err)
		// Clean up git worktree via daemon
		_ = s.sendWorktreeDaemonCommand(ctx, userID, "worktree.delete_directory", map[string]string{
			"project_path":  project.Path,
			"worktree_path": worktreePath,
		}, nil)

		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project; enable force to override", req.Msg.Name))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create worktree"))
	}

	return connect.NewResponse(&reliantv1.CreateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// handleForceCreate handles cleanup for force create mode
func (s *WorktreeService) handleForceCreate(ctx context.Context, userID, projectPath, name, branch, projectID string) error {
	// Check if worktree exists in database and delete it
	existingWorktrees, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &projectID,
		Limit:     1000,
	})
	if err == nil {
		for _, wt := range existingWorktrees {
			if wt.Name == name {
				if err := s.database.DeleteWorktree(ctx, wt.ID); err != nil {
					logging.Warn("Force mode: failed to delete existing worktree from database", "error", err)
				}
				if wt.Path != "" {
					_ = s.sendWorktreeDaemonCommand(ctx, userID, "worktree.delete_directory", map[string]string{
						"project_path":  projectPath,
						"worktree_path": wt.Path,
					}, nil)
				}
				break
			}
		}
	}

	// Send force cleanup command to daemon (handles worktree remove, prune, branch delete)
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.force_cleanup", map[string]string{
		"project_path":  projectPath,
		"worktree_path": "", // Path will be constructed by daemon during create
		"branch":        branch,
	}, nil); err != nil {
		logging.Warn("Force mode: daemon cleanup failed", "error", err)
	}

	return nil
}

// parseGitWorktreeError parses git worktree errors into user-friendly messages
func (s *WorktreeService) parseGitWorktreeError(output, worktreePath, branch, baseBranch string) error {
	switch {
	case strings.Contains(output, "already exists") && strings.Contains(output, "not empty"):
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree path '%s' already exists and is not empty; enable force to override", worktreePath))
	case strings.Contains(output, "already checked out"):
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("branch '%s' is already checked out in another worktree; enable force to override or choose a different branch", branch))
	case strings.Contains(output, "is not a valid"):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("base branch '%s' does not exist; please choose a valid branch", baseBranch))
	case strings.Contains(output, "fatal: invalid reference"):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid branch or reference '%s'; please check the branch name", baseBranch))
	case strings.Contains(output, "Not a git repository"):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project directory is not a valid git repository; please initialize git first"))
	default:
		errMsg := strings.TrimSpace(output)
		if errMsg == "" {
			errMsg = "unknown git error"
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("git worktree creation failed: %s", errMsg))
	}
}

// ListWorktrees lists worktrees for a project
func (s *WorktreeService) ListWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorktreesRequest],
) (*connect.Response[reliantv1.ListWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Check project permission
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	projectID := req.Msg.ProjectId
	filters := db.WorktreeFilters{
		ProjectID:       &projectID,
		IncludeArchived: req.Msg.GetIncludeArchived(),
		Limit:           100,
	}

	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		filters.ChatID = req.Msg.ChatId
	}

	if req.Msg.Limit > 0 {
		filters.Limit = int(req.Msg.Limit)
	}

	worktrees, err := s.database.ListWorktrees(ctx, filters)
	if err != nil {
		logging.Error("Failed to list worktrees", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list worktrees"))
	}

	// Self-heal invariant: every project should have a main worktree.
	// This also repairs older projects created while Postgres parity bugs existed.
	if len(worktrees) == 0 {
		project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}

		defaultBranch := "main"
		if project.DefaultBranch != nil && *project.DefaultBranch != "" {
			defaultBranch = *project.DefaultBranch
		}

		now := time.Now().UTC()
		mainWorktree := &db.Worktree{
			ID:         uuid.New().String(),
			Name:       defaultBranch,
			Path:       project.Path,
			Branch:     defaultBranch,
			BaseBranch: defaultBranch,
			ProjectID:  project.ID,
			Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
			IsMain:     true,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}

		if err := s.database.CreateWorktree(ctx, mainWorktree); err != nil {
			logging.Warn("Failed to auto-create main worktree during ListWorktrees", "error", err, "projectID", project.ID)
			// Re-fetch in case another request created it concurrently.
			worktrees, err = s.database.ListWorktrees(ctx, filters)
			if err != nil || len(worktrees) == 0 {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to ensure main worktree"))
			}
		} else {
			worktrees = []*db.Worktree{mainWorktree}
		}
	}

	protoWorktrees := make([]*reliantv1.Worktree, len(worktrees))
	for i, w := range worktrees {
		protoWorktrees[i] = worktreeToProto(w)
	}

	return connect.NewResponse(&reliantv1.ListWorktreesResponse{
		Worktrees: protoWorktrees,
		Total:     int32(len(protoWorktrees)),
	}), nil
}

// GetWorktree retrieves a worktree by ID
func (s *WorktreeService) GetWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeRequest],
) (*connect.Response[reliantv1.GetWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	return connect.NewResponse(&reliantv1.GetWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// UpdateWorktree updates a worktree
func (s *WorktreeService) UpdateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorktreeRequest],
) (*connect.Response[reliantv1.UpdateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if req.Msg.Name != nil {
		worktree.Name = *req.Msg.Name
	}
	if req.Msg.Status != nil {
		worktree.Status = int32(*req.Msg.Status)
	}
	if req.Msg.BaseBranch != nil {
		worktree.BaseBranch = *req.Msg.BaseBranch
	}
	now := time.Now().UTC()
	worktree.UpdatedAt = now
	worktree.LastActive = now

	if err := s.database.UpdateWorktree(ctx, worktree); err != nil {
		logging.Error("Failed to update worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update worktree"))
	}

	return connect.NewResponse(&reliantv1.UpdateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// DeleteWorktree deletes or archives a worktree based on its current state
func (s *WorktreeService) DeleteWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteWorktreeRequest],
) (*connect.Response[reliantv1.DeleteWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	// Prevent deletion of main worktree
	if worktree.IsMain {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete the main project worktree"))
	}

	project, err := s.database.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.Warn("Failed to get project for worktree cleanup", "error", err)
	}

	isPermanentDelete := worktree.DeletedAt != nil

	// Perform cleanup
	deletedDir := false
	deletedBranch := false

	if req.Msg.DeleteLocalDirectory && project != nil && worktree.Path != "" {
		deletedDir = s.cleanupWorktreeDirectory(ctx, userID, project.Path, worktree.Path)
	}

	if req.Msg.DeleteGitBranch && project != nil && worktree.Branch != "" {
		deletedBranch = s.cleanupWorktreeBranch(ctx, userID, project.Path, worktree.Branch)
	}

	if isPermanentDelete {
		// Permanently delete from database
		if err := s.database.DeleteWorktree(ctx, req.Msg.WorktreeId); err != nil {
			logging.Error("Failed to permanently delete worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to permanently delete worktree"))
		}

		return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
			Message:           "Worktree permanently deleted",
			DeletedDirectory:  deletedDir,
			DeletedBranch:     deletedBranch,
			IsPermanentDelete: true,
		}), nil
	}

	// Store cleanup metadata
	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, deletedDir, deletedBranch)

	// Archive the worktree
	if err := s.database.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to archive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)

	return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
		Message:           "Worktree and associated chats archived successfully",
		DeletedDirectory:  deletedDir,
		DeletedBranch:     deletedBranch,
		IsPermanentDelete: false,
	}), nil
}

// ArchiveWorktree archives a worktree (dedicated endpoint)
func (s *WorktreeService) ArchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ArchiveWorktreeRequest],
) (*connect.Response[reliantv1.ArchiveWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if worktree.IsMain {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot archive the main project worktree"))
	}

	if worktree.DeletedAt != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree is already archived; use delete to permanently remove"))
	}

	project, err := s.database.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.Warn("Failed to get project for worktree cleanup", "error", err)
	}

	// Perform cleanup
	deletedDir := false
	deletedBranch := false

	if req.Msg.DeleteLocalDirectory && project != nil && worktree.Path != "" {
		deletedDir = s.cleanupWorktreeDirectory(ctx, userID, project.Path, worktree.Path)
	}

	if req.Msg.DeleteGitBranch && project != nil && worktree.Branch != "" {
		deletedBranch = s.cleanupWorktreeBranch(ctx, userID, project.Path, worktree.Branch)
	}

	// Store cleanup metadata
	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, deletedDir, deletedBranch)

	// Archive the worktree
	if err := s.database.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to archive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)

	return connect.NewResponse(&reliantv1.ArchiveWorktreeResponse{
		Message:          "Worktree and associated chats archived successfully",
		DeletedDirectory: deletedDir,
		DeletedBranch:    deletedBranch,
	}), nil
}

// UnarchiveWorktree restores an archived worktree
func (s *WorktreeService) UnarchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UnarchiveWorktreeRequest],
) (*connect.Response[reliantv1.UnarchiveWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if worktree.DeletedAt == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree is not archived"))
	}

	if err := s.database.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to unarchive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unarchive worktree"))
	}

	// Unarchive associated chats
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	return connect.NewResponse(&reliantv1.UnarchiveWorktreeResponse{
		Message: "Worktree and associated chats unarchived successfully",
	}), nil
}

// =============================================================================
// Cleanup Helpers
// =============================================================================

func (s *WorktreeService) cleanupWorktreeDirectory(ctx context.Context, userID, projectPath, worktreePath string) bool {
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.delete_directory", map[string]string{
		"project_path":  projectPath,
		"worktree_path": worktreePath,
	}, &resp); err != nil {
		logging.Warn("Failed to remove git worktree via daemon", "error", err, "path", worktreePath)
		return false
	}
	return resp.Deleted
}

func (s *WorktreeService) cleanupWorktreeBranch(ctx context.Context, userID, projectPath, branch string) bool {
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.delete_branch", map[string]string{
		"project_path": projectPath,
		"branch":       branch,
	}, &resp); err != nil {
		logging.Warn("Failed to delete git branch via daemon", "error", err, "branch", branch)
		return false
	}
	return resp.Deleted
}

func (s *WorktreeService) storeCleanupMetadata(ctx context.Context, worktreeID string, deletedDir, deletedBranch bool) {
	if !deletedDir && !deletedBranch {
		return
	}

	metadata := db.CleanupMetadata{
		DirectoryDeleted: deletedDir,
		BranchDeleted:    deletedBranch,
	}
	err := s.database.UpdateWorktreeCleanupMetadata(ctx, worktreeID, &metadata)
	if err != nil {
		logging.Warn("Failed to store cleanup metadata", "error", err, "worktreeID", worktreeID)
	}
}

func (s *WorktreeService) archiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	chats, err := s.database.ListChats(ctx, db.ChatFilters{
		UserID:          userID,
		ProjectID:       &worktree.ProjectID,
		ExcludeArchived: true,
		Limit:           1000,
	})
	if err != nil {
		logging.Warn("Failed to list chats for archiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			// Cancel workflow if running (best-effort)
			if chat.WorkflowID != nil && *chat.WorkflowID != "" {
				_ = s.tempClient.CancelWorkflow(ctx, *chat.WorkflowID, "")
			}
			if err := s.database.UpdateChatState(ctx, chat.ID, db.ChatStateArchived, "worktree_archived"); err != nil {
				logging.Warn("Failed to archive chat", "error", err, "chatID", chat.ID)
			}
		}
	}
}

func (s *WorktreeService) unarchiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	archivedState := db.ChatStateArchived
	chats, err := s.database.ListChats(ctx, db.ChatFilters{
		UserID:    userID,
		ProjectID: &worktree.ProjectID,
		State:     &archivedState,
		Limit:     1000,
	})
	if err != nil {
		logging.Warn("Failed to list chats for unarchiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			if err := s.database.UpdateChatState(ctx, chat.ID, db.ChatStateIdle, "worktree_unarchived"); err != nil {
				logging.Warn("Failed to unarchive chat", "error", err, "chatID", chat.ID)
			}
		}
	}
}

// =============================================================================
// Import/Discovery Operations
// =============================================================================

// ImportWorktree imports an existing git worktree directory
func (s *WorktreeService) ImportWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ImportWorktreeRequest],
) (*connect.Response[reliantv1.ImportWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path is required"))
	}
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Check project permission
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Validate path exists and is a git worktree via daemon
	var importResp struct {
		Valid      bool   `json:"valid"`
		AbsPath    string `json:"abs_path"`
		Branch     string `json:"branch"`
		BaseBranch string `json:"base_branch"`
		Error      string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.import_validate", map[string]string{"path": req.Msg.Path}, &importResp); err != nil {
		logging.Error("Failed to validate import path via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to validate path"))
	}
	if !importResp.Valid {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", importResp.Error))
	}

	absPath := importResp.AbsPath
	branch := importResp.Branch
	baseBranch := importResp.BaseBranch

	// Determine worktree name
	name := ""
	if req.Msg.Name != nil && *req.Msg.Name != "" {
		name = *req.Msg.Name
	} else {
		// Extract base name from absolute path
		parts := strings.Split(absPath, "/")
		if len(parts) > 0 {
			name = parts[len(parts)-1]
		} else {
			name = absPath
		}
	}

	// Check if worktree with this name or path already exists for this project
	existingWorktrees, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &req.Msg.ProjectId,
		Limit:     1000,
	})
	if err != nil {
		logging.Error("Failed to list worktrees", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check for existing worktrees"))
	}

	for _, wt := range existingWorktrees {
		if wt.Name == name {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project", name))
		}
		if wt.Path == absPath {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree at path '%s' is already imported", absPath))
		}
	}

	worktreeID := uuid.New().String()
	now := time.Now().UTC()

	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}

	worktree := &db.Worktree{
		ID:         worktreeID,
		Name:       name,
		Path:       absPath,
		Branch:     branch,
		BaseBranch: baseBranch,
		ProjectID:  req.Msg.ProjectId,
		ChatID:     chatID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}

	if err := s.database.CreateWorktree(ctx, worktree); err != nil {
		logging.Error("Failed to import worktree", "error", err)
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project", name))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to import worktree"))
	}

	return connect.NewResponse(&reliantv1.ImportWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// DiscoverWorktrees discovers existing git worktrees in a project
func (s *WorktreeService) DiscoverWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.DiscoverWorktreesRequest],
) (*connect.Response[reliantv1.DiscoverWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Check project permission
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Get the project
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Discover worktrees via daemon
	type discoverEntry struct {
		Path       string `json:"path"`
		Name       string `json:"name"`
		Branch     string `json:"branch"`
		IsPrunable bool   `json:"is_prunable"`
	}
	var discoverResp struct {
		Worktrees []discoverEntry `json:"worktrees"`
		Error     string          `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.discover", map[string]string{"project_path": project.Path}, &discoverResp); err != nil {
		logging.Warn("Failed to discover worktrees via daemon", "error", err)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("failed to list git worktrees"))
	}
	if discoverResp.Error != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("git error: %s", discoverResp.Error))
	}

	// Filter out prunable worktrees
	var filtered []discoverEntry
	for _, wt := range discoverResp.Worktrees {
		if wt.IsPrunable {
			continue
		}
		filtered = append(filtered, wt)
	}

	// Get existing worktrees to mark which are imported
	existingWorktrees, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &req.Msg.ProjectId,
		Limit:     1000,
	})
	if err != nil {
		logging.Warn("Failed to load existing worktrees", "error", err)
	}

	// Build import lookup map
	importedByPath := make(map[string]*db.Worktree)
	for _, wt := range existingWorktrees {
		importedByPath[wt.Path] = wt
	}

	// Convert to proto messages
	protoDiscovered := make([]*reliantv1.DiscoveredWorktree, len(filtered))
	for i, wt := range filtered {
		proto := &reliantv1.DiscoveredWorktree{
			Path:       wt.Path,
			Name:       wt.Name,
			Branch:     wt.Branch,
			IsImported: false,
			IsPrunable: wt.IsPrunable,
		}
		if existing, ok := importedByPath[wt.Path]; ok {
			proto.IsImported = true
			proto.ImportedId = &existing.ID
		}
		protoDiscovered[i] = proto
	}

	return connect.NewResponse(&reliantv1.DiscoverWorktreesResponse{
		Discovered: protoDiscovered,
		Total:      int32(len(protoDiscovered)),
	}), nil
}

// RecreateWorktree recreates an archived worktree from its branch
func (s *WorktreeService) RecreateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.RecreateWorktreeRequest],
) (*connect.Response[reliantv1.RecreateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	// Check permission
	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	// Get worktree to verify it exists and is archived
	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if worktree.DeletedAt == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree is not archived; only archived worktrees can be recreated"))
	}

	// Get project for git operations
	project, err := s.database.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", worktree.ProjectID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Recreate worktree via daemon (checks branch, path, creates dir, runs git worktree add)
	var recreateResp struct {
		Success      bool   `json:"success"`
		BranchExists bool   `json:"branch_exists"`
		PathExists   bool   `json:"path_exists"`
		Output       string `json:"output,omitempty"`
		Error        string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.recreate", map[string]string{
		"project_path":  project.Path,
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
	}, &recreateResp); err != nil {
		logging.Error("Failed to recreate worktree via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate worktree"))
	}
	if !recreateResp.Success {
		if !recreateResp.BranchExists {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("branch '%s' no longer exists; cannot recreate worktree", worktree.Branch))
		}
		if recreateResp.PathExists {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree directory '%s' already exists", worktree.Path))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate worktree: %s", recreateResp.Error))
	}

	// Clear cleanup metadata since files are now restored
	err = s.database.UpdateWorktreeCleanupMetadata(ctx, req.Msg.WorktreeId, nil)
	if err != nil {
		logging.Warn("Failed to clear cleanup metadata", "error", err)
	}

	// Unarchive the worktree
	if err := s.database.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to unarchive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unarchive worktree"))
	}

	// Unarchive all chats associated with this worktree
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	return connect.NewResponse(&reliantv1.RecreateWorktreeResponse{
		Message: "Worktree recreated successfully from branch",
		Path:    worktree.Path,
		Branch:  worktree.Branch,
	}), nil
}

// =============================================================================
// Git Read Operations
// =============================================================================

// GetWorktreeChanges gets file changes for a worktree
func (s *WorktreeService) GetWorktreeChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeChangesRequest],
) (*connect.Response[reliantv1.GetWorktreeChangesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.git_changes", map[string]string{
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
		"base_branch":   worktree.BaseBranch,
	}, &changesResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get worktree changes: %w", err))
	}
	if changesResp.Error != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", changesResp.Error))
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

// GetWorktreeGitStatus gets the overall git status for a worktree
func (s *WorktreeService) GetWorktreeGitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeGitStatusRequest],
) (*connect.Response[reliantv1.GetWorktreeGitStatusResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Get git status via daemon
	var statusResp struct {
		Branch         string   `json:"branch"`
		HasChanges     bool     `json:"has_changes"`
		Status         string   `json:"status"`
		StagedFiles    []string `json:"staged_files"`
		UnstagedFiles  []string `json:"unstaged_files"`
		UntrackedFiles []string `json:"untracked_files"`
		Ahead          int32    `json:"ahead"`
		Behind         int32    `json:"behind"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.git_status", map[string]string{
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
	}, &statusResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get git status: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetWorktreeGitStatusResponse{
		WorktreeId:     worktree.ID,
		Path:           worktree.Path,
		Branch:         statusResp.Branch,
		Clean:          statusResp.Status == "clean",
		HasChanges:     statusResp.HasChanges,
		StagedFiles:    statusResp.StagedFiles,
		ModifiedFiles:  statusResp.UnstagedFiles,
		UntrackedFiles: statusResp.UntrackedFiles,
		Ahead:          statusResp.Ahead,
		Behind:         statusResp.Behind,
	}), nil
}

// GetWorktreeCommits gets commit history for a worktree
func (s *WorktreeService) GetWorktreeCommits(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeCommitsRequest],
) (*connect.Response[reliantv1.GetWorktreeCommitsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Get commit limit (default 20, max 100)
	limit := int32(20)
	if req.Msg.Limit > 0 && req.Msg.Limit <= 100 {
		limit = req.Msg.Limit
	}

	// Determine base branch
	baseBranch := worktree.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Get commits via daemon
	type commitEntry struct {
		Hash      string `json:"hash"`
		ShortHash string `json:"short_hash"`
		Author    string `json:"author"`
		Email     string `json:"email"`
		Date      string `json:"date"`
		Message   string `json:"message"`
	}
	var commitsResp struct {
		Commits        []commitEntry `json:"commits"`
		Total          int32         `json:"total"`
		Branch         string        `json:"branch"`
		BaseBranch     string        `json:"base_branch"`
		ComparisonMode bool          `json:"comparison_mode"`
		ComparisonRef  string        `json:"comparison_ref"`
		CurrentBranch  string        `json:"current_branch"`
	}
	type commitsReq struct {
		WorktreePath string `json:"worktree_path"`
		Branch       string `json:"branch"`
		BaseBranch   string `json:"base_branch"`
		Limit        int32  `json:"limit"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.git_commits", commitsReq{
		WorktreePath: worktree.Path,
		Branch:       worktree.Branch,
		BaseBranch:   baseBranch,
		Limit:        limit,
	}, &commitsResp); err != nil {
		logging.Warn("Failed to get git commits via daemon", "error", err)
		return connect.NewResponse(&reliantv1.GetWorktreeCommitsResponse{
			Commits: []*reliantv1.GitCommit{},
		}), nil
	}

	// Convert to proto
	commits := make([]*reliantv1.GitCommit, len(commitsResp.Commits))
	for i, c := range commitsResp.Commits {
		commits[i] = &reliantv1.GitCommit{
			Hash:      c.Hash,
			ShortHash: c.ShortHash,
			Author:    c.Author,
			Email:     c.Email,
			Date:      c.Date,
			Message:   c.Message,
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreeCommitsResponse{
		Commits:        commits,
		Total:          commitsResp.Total,
		Branch:         worktree.Branch,
		BaseBranch:     worktree.BaseBranch,
		ComparisonMode: commitsResp.ComparisonMode,
		ComparisonRef:  commitsResp.ComparisonRef,
		CurrentBranch:  commitsResp.CurrentBranch,
	}), nil
}

// =============================================================================
// Git Write Operations
// =============================================================================

// StageFiles stages files in a worktree
func (s *WorktreeService) StageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.StageFilesRequest],
) (*connect.Response[reliantv1.StageFilesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.stage", stageReq{
		WorktreePath: worktree.Path,
		Files:        req.Msg.Files,
	}, &stageResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stage files: %w", err))
	}
	if !stageResp.Success {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stage files: %s", stageResp.Error))
	}

	return connect.NewResponse(&reliantv1.StageFilesResponse{
		Message: "Files staged successfully",
		Files:   req.Msg.Files,
	}), nil
}

// UnstageFiles unstages files in a worktree
func (s *WorktreeService) UnstageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.UnstageFilesRequest],
) (*connect.Response[reliantv1.UnstageFilesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Unstage files via daemon
	type unstageReq struct {
		WorktreePath string   `json:"worktree_path"`
		Files        []string `json:"files"`
	}
	var unstageResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.unstage", unstageReq{
		WorktreePath: worktree.Path,
		Files:        req.Msg.Files,
	}, &unstageResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unstage files: %w", err))
	}
	if !unstageResp.Success {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unstage files: %s", unstageResp.Error))
	}

	return connect.NewResponse(&reliantv1.UnstageFilesResponse{
		Message: "Files unstaged successfully",
		Files:   req.Msg.Files,
	}), nil
}

// CommitWorktree commits staged changes in a worktree via daemon
func (s *WorktreeService) CommitWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.CommitWorktreeRequest],
) (*connect.Response[reliantv1.CommitWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if req.Msg.Message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("commit message is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Commit via daemon
	output, err := s.commitViaDaemon(ctx, userID, worktree.Path, req.Msg.Message)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "nothing to commit") || strings.Contains(errStr, "nothing added to commit") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no staged changes to commit; stage files first"))
		}
		logging.Error("Failed to commit changes", "error", err, "path", worktree.Path)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to commit: %s", errStr))
	}

	return connect.NewResponse(&reliantv1.CommitWorktreeResponse{
		Message: "Changes committed successfully",
		Output:  output,
	}), nil
}

// PushWorktree pushes changes from a worktree to remote
func (s *WorktreeService) PushWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.PushWorktreeRequest],
) (*connect.Response[reliantv1.PushWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Push via daemon
	output, err := s.pushViaDaemon(ctx, userID, worktree.Path, worktree.Branch)
	if err != nil {
		logging.Error("Failed to push changes", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to push: %s", err.Error()))
	}

	return connect.NewResponse(&reliantv1.PushWorktreeResponse{
		Message: "Changes pushed successfully",
		Output:  output,
	}), nil
}

// PullWorktree pulls changes from remote
func (s *WorktreeService) PullWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.PullWorktreeRequest],
) (*connect.Response[reliantv1.PullWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Pull via daemon
	output, err := s.pullViaDaemon(ctx, userID, worktree.Path, worktree.Branch)
	if err != nil {
		logging.Error("Failed to pull changes", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to pull: %s", err.Error()))
	}

	return connect.NewResponse(&reliantv1.PullWorktreeResponse{
		Message: "Changes pulled successfully",
		Output:  output,
	}), nil
}

// GetWorktreePR checks if a PR already exists for the worktree's branch
func (s *WorktreeService) GetWorktreePR(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreePRRequest],
) (*connect.Response[reliantv1.GetWorktreePRResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Get PR info via daemon
	var prResp struct {
		Exists     bool   `json:"exists"`
		URL        string `json:"url,omitempty"`
		Number     int32  `json:"number,omitempty"`
		Title      string `json:"title,omitempty"`
		State      string `json:"state,omitempty"`
		LocalHead  string `json:"local_head,omitempty"`
		HeadRefOid string `json:"head_ref_oid,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.get_pr", map[string]string{
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
	}, &prResp); err != nil {
		logging.Warn("Failed to check PR via daemon", "error", err)
		return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
	}

	if !prResp.Exists {
		return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
	}

	// If the PR is not OPEN, check if commits match
	if prResp.State != "OPEN" {
		if prResp.LocalHead != "" && prResp.HeadRefOid != "" && prResp.LocalHead != prResp.HeadRefOid {
			return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreePRResponse{
		Exists: true,
		Url:    &prResp.URL,
		Number: &prResp.Number,
		Title:  &prResp.Title,
		State:  &prResp.State,
	}), nil
}

// CreateWorktreePR creates a pull request for a worktree
// It automatically stages, commits, and pushes changes if needed for a seamless PR experience
func (s *WorktreeService) CreateWorktreePR(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorktreePRRequest],
) (*connect.Response[reliantv1.CreateWorktreePRResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if req.Msg.Title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("PR title is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Route entire create-PR flow through daemon (stage, commit, push, create PR)
	body := ""
	if req.Msg.Body != nil && *req.Msg.Body != "" {
		body = *req.Msg.Body
	}

	var prResp struct {
		Success       bool   `json:"success"`
		PRURL         string `json:"pr_url,omitempty"`
		Output        string `json:"output,omitempty"`
		AutoCommitted bool   `json:"auto_committed"`
		AutoPushed    bool   `json:"auto_pushed"`
		Error         string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.create_pr", map[string]string{
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
		"title":         req.Msg.Title,
		"body":          body,
	}, &prResp); err != nil {
		logging.Error("Failed to create PR via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create PR: %w", err))
	}

	if !prResp.Success {
		errMsg := prResp.Error
		if strings.Contains(errMsg, "cannot create a pull request from the default branch") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", errMsg))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", errMsg))
	}

	// Build informative message
	message := "Pull request created successfully"
	if prResp.AutoCommitted && prResp.AutoPushed {
		message = "Changes committed and pushed, pull request created successfully"
	} else if prResp.AutoCommitted {
		message = "Changes committed, pull request created successfully"
	} else if prResp.AutoPushed {
		message = "Branch pushed, pull request created successfully"
	}

	return connect.NewResponse(&reliantv1.CreateWorktreePRResponse{
		Message:       message,
		PrUrl:         prResp.PRURL,
		Output:        prResp.Output,
		AutoCommitted: prResp.AutoCommitted,
		AutoPushed:    prResp.AutoPushed,
	}), nil
}

// RevertFiles reverts/discards file changes via daemon
func (s *WorktreeService) RevertFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.RevertFilesRequest],
) (*connect.Response[reliantv1.RevertFilesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	if len(req.Msg.Files) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one file is required"))
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.database.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		logging.Error("Failed to get worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
	}

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	// Route revert through daemon
	type revertResult struct {
		File    string `json:"file"`
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	var revertResp struct {
		Results []revertResult `json:"results"`
		Error   string         `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.revert", map[string]interface{}{
		"worktree_path": worktree.Path,
		"files":         req.Msg.Files,
	}, &revertResp); err != nil {
		logging.Error("Failed to revert files via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revert files: %w", err))
	}

	if revertResp.Error != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", revertResp.Error))
	}

	var revertedFiles []string
	var errors []string
	for _, r := range revertResp.Results {
		if r.Success {
			revertedFiles = append(revertedFiles, r.File)
		} else {
			errors = append(errors, fmt.Sprintf("%s: %s", r.File, r.Error))
		}
	}

	if len(revertedFiles) == 0 && len(errors) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("failed to revert requested files: %s", strings.Join(errors, "; ")))
	}

	message := fmt.Sprintf("Reverted %d file(s)", len(revertedFiles))
	if len(errors) > 0 {
		message = fmt.Sprintf("%s, %d error(s): %s", message, len(errors), strings.Join(errors, "; "))
	}

	return connect.NewResponse(&reliantv1.RevertFilesResponse{
		Message: message,
		Files:   revertedFiles,
	}), nil
}
