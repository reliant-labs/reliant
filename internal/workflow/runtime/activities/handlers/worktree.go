// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// DeleteWorktreeInput is the input for DeleteWorktree activity.
// DeleteWorktree is a utility activity (no node type), so it uses a flat input.
type DeleteWorktreeInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	// Name is the worktree name to delete.
	Name string `json:"name"`
}

// DeleteWorktreeOutput is defined in types.go as an alias for v3.DeleteWorktreeOutput

// ============================================================================
// ACTIVITY IMPLEMENTATION - CreateWorktree
// ============================================================================

// CreateWorktreeActivity implements the create_worktree activity.
// This activity creates a new git worktree for a chat's project
// by routing git operations through the user's daemon via DaemonRouter.
type CreateWorktreeActivity struct {
	repo         db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewCreateWorktreeActivity creates a new CreateWorktreeActivity
func NewCreateWorktreeActivity(repo db.Repository, daemonRouter toolexec.DaemonRouter) *CreateWorktreeActivity {
	return &CreateWorktreeActivity{
		repo:         repo,
		daemonRouter: daemonRouter,
	}
}

// Name returns the activity name for registration
func (a *CreateWorktreeActivity) Name() string {
	return "CreateWorktree"
}

// DisplayName returns human-readable name for UI
func (a *CreateWorktreeActivity) DisplayName() string {
	return "Create Worktree"
}

// Description returns what the activity does
func (a *CreateWorktreeActivity) Description() string {
	return "Create a new git worktree for isolated parallel development"
}

// Category returns the activity category for UI grouping
func (a *CreateWorktreeActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorktree
}

// Execute contains PURE BUSINESS LOGIC only
func (a *CreateWorktreeActivity) Execute(ctx context.Context, input ActivityInput) (CreateWorktreeOutput, error) {
	if a.daemonRouter == nil {
		return CreateWorktreeOutput{}, fmt.Errorf("daemon router not available; worktree creation requires a daemon connection")
	}

	rtx := input.Runtime
	protoArgs := model.GetCreateWorktreeArgs(input.Node)
	if protoArgs == nil {
		return CreateWorktreeOutput{}, fmt.Errorf("expected create_worktree node, got %s", model.NodeType(input.Node))
	}

	// Get chat to access project and userID
	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to get chat: %w", err)
	}

	if chat.ProjectID == "" {
		return CreateWorktreeOutput{}, fmt.Errorf("chat has no project_id")
	}

	// Get project to access working directory
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to get project: %w", err)
	}

	// Generate repo ID via daemon
	repoID, err := sendWorktreeDaemonCmd[generateRepoIDResponse](
		ctx, a.daemonRouter, chat.UserID, "worktree.generate_repo_id",
		map[string]string{"project_path": project.Path}, 30_000,
	)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to generate repo ID: %w", err)
	}

	// Determine branch name
	name := model.CelStringValue(protoArgs.GetName())
	branch := model.CelStringValue(protoArgs.GetBranch())
	baseBranch := model.CelStringValue(protoArgs.GetBaseBranch())
	force := model.CelBoolValue(protoArgs.GetForce())

	// Generate default branch name if not provided (must match daemon-side logic)
	if branch == "" {
		branch = fmt.Sprintf("worktree/%s-%d", name, time.Now().Unix())
	}

	// Create worktree via daemon
	createResp, err := sendWorktreeDaemonCmd[worktreeCreateDaemonResponse](
		ctx, a.daemonRouter, chat.UserID, "worktree.create",
		worktreeCreateDaemonRequest{
			ProjectPath: project.Path,
			RepoID:      repoID.RepoID,
			Name:        name,
			Branch:      branch,
			BaseBranch:  baseBranch,
			Force:       force,
			CopyFiles:   protoArgs.GetCopyFiles(),
		}, 60_000,
	)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to create worktree via daemon: %w", err)
	}
	if !createResp.Success {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to create worktree: %s", createResp.Error)
	}

	worktreeID := fmt.Sprintf("%s/%s", repoID.RepoID, name)

	return CreateWorktreeOutput{
		Id:         worktreeID,
		Name:       name,
		Path:       createResp.WorktreePath,
		Branch:     branch,
		BaseBranch: baseBranch,
		RepoId:     repoID.RepoID,
		Status:     "active",
	}, nil
}

// ============================================================================
// ACTIVITY IMPLEMENTATION - DeleteWorktree
// ============================================================================

// DeleteWorktreeActivity implements TypedActivity[DeleteWorktreeInput, DeleteWorktreeOutput]
// This activity deletes a git worktree for a chat's project
// by routing git operations through the user's daemon via DaemonRouter.
type DeleteWorktreeActivity struct {
	repo         db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewDeleteWorktreeActivity creates a new DeleteWorktreeActivity
func NewDeleteWorktreeActivity(repo db.Repository, daemonRouter toolexec.DaemonRouter) *DeleteWorktreeActivity {
	return &DeleteWorktreeActivity{
		repo:         repo,
		daemonRouter: daemonRouter,
	}
}

// Name returns the activity name for registration
func (a *DeleteWorktreeActivity) Name() string {
	return "DeleteWorktree"
}

// DisplayName returns human-readable name for UI
func (a *DeleteWorktreeActivity) DisplayName() string {
	return "Delete Worktree"
}

// Description returns what the activity does
func (a *DeleteWorktreeActivity) Description() string {
	return "Delete a git worktree and clean up its resources"
}

// Category returns the activity category for UI grouping
func (a *DeleteWorktreeActivity) Category() schema.ActivityCategory {
	return schema.CategoryGit
}

// Execute contains PURE BUSINESS LOGIC only
func (a *DeleteWorktreeActivity) Execute(ctx context.Context, input DeleteWorktreeInput) (DeleteWorktreeOutput, error) {
	if a.daemonRouter == nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("daemon router not available; worktree deletion requires a daemon connection")
	}

	// Get chat to access project and userID
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("failed to get chat: %w", err)
	}

	if chat.ProjectID == "" {
		return DeleteWorktreeOutput{}, fmt.Errorf("chat has no project_id")
	}

	// Get project to access working directory
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("failed to get project: %w", err)
	}

	// Look up the worktree record to get its path
	projectID := chat.ProjectID
	worktrees, err := a.repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &projectID,
	})
	if err != nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("failed to look up worktree: %w", err)
	}

	var worktreePath string
	for _, wt := range worktrees {
		if wt.Name == input.Name {
			worktreePath = wt.Path
			break
		}
	}
	if worktreePath == "" {
		return DeleteWorktreeOutput{}, fmt.Errorf("worktree '%s' not found", input.Name)
	}

	// Delete worktree via daemon
	deleteResp, err := sendWorktreeDaemonCmd[worktreeDeleteDaemonResponse](
		ctx, a.daemonRouter, chat.UserID, "worktree.delete_directory",
		worktreeDeleteDaemonRequest{
			ProjectPath:  project.Path,
			WorktreePath: worktreePath,
		}, 30_000,
	)
	if err != nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("failed to delete worktree via daemon: %w", err)
	}

	return DeleteWorktreeOutput{
		Deleted: deleteResp.Deleted,
	}, nil
}

// ============================================================================
// DAEMON COMMAND TYPES & HELPERS
// ============================================================================

type generateRepoIDResponse struct {
	RepoID string `json:"repo_id"`
}

type worktreeCreateDaemonRequest struct {
	ProjectPath string   `json:"project_path"`
	RepoID      string   `json:"repo_id"`
	Name        string   `json:"name"`
	Branch      string   `json:"branch"`
	BaseBranch  string   `json:"base_branch"`
	Force       bool     `json:"force"`
	CopyFiles   []string `json:"copy_files,omitempty"`
}

type worktreeCreateDaemonResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Error        string `json:"error,omitempty"`
}

type worktreeDeleteDaemonRequest struct {
	ProjectPath  string `json:"project_path"`
	WorktreePath string `json:"worktree_path"`
}

type worktreeDeleteDaemonResponse struct {
	Deleted bool `json:"deleted"`
}

// sendWorktreeDaemonCmd marshals a request, sends it via DaemonRouter, and unmarshals the response.
func sendWorktreeDaemonCmd[T any](ctx context.Context, router toolexec.DaemonRouter, userID, commandType string, payload interface{}, timeoutMs int32) (T, error) {
	var zero T
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return zero, fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := router.SendDaemonCommand(ctx, userID, commandType, payloadBytes, timeoutMs)
	if err != nil {
		return zero, fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	var resp T
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return zero, fmt.Errorf("unmarshal response for %s: %w", commandType, err)
	}
	return resp, nil
}
