// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

const worktreeCreateDaemonTimeoutMs int32 = 120_000

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

// Execute creates a workspace-level worktree spanning every nested repo of
// the chat's project. Mirrors the gRPC WorktreeService.CreateWorktree flow:
// fan out one daemon worktree.create per repo (all under a single
// workspace UUID), persist a single Worktree row whose Path points at the
// workspace root, best-effort rollback on partial failure.
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

	// Enumerate the project's nested repos. A standalone-repo project has
	// exactly one Repo with RelativePath == "" — that legacy single-repo
	// shape continues to work without changes here.
	repos, err := a.repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to list repos for project: %w", err)
	}
	if len(repos) == 0 {
		// Same self-heal as the gRPC gate: the registry trails the filesystem
		// when a repo was created outside the tracked flows (e.g. a manual
		// `git init`), so adopt what actually exists before refusing.
		repos = repopkg.AdoptFromDaemon(ctx, a.repo, a.daemonRouter, project)
	}
	if len(repos) == 0 {
		return CreateWorktreeOutput{}, fmt.Errorf("project has no git repos; initialize one or add a nested repo before creating worktrees")
	}

	// Resolve args
	name := model.CelStringValue(protoArgs.GetName())
	branch := model.CelStringValue(protoArgs.GetBranch())
	baseBranch := model.CelStringValue(protoArgs.GetBaseBranch())
	force := model.CelBoolValue(protoArgs.GetForce())
	copyFiles := protoArgs.GetCopyFiles()

	// Generate default branch name if not provided (must match daemon-side logic)
	if branch == "" {
		branch = fmt.Sprintf("worktree/%s-%d", name, time.Now().Unix())
	}

	// Human-readable workspace dir name; matches the convention used by the
	// CreateWorktree gRPC handler so disk paths look the same regardless of
	// where the worktree was kicked off.
	workspaceID := worktreepath.WorkspaceDirName(project.Name, name)

	type repoCreateResult struct {
		repo         *core.Repo
		worktreePath string
		baseBranch   string
	}
	successes := make([]repoCreateResult, 0, len(repos))

	rollback := func(reason error) error {
		for _, s := range successes {
			repoPath := filepath.Join(project.Path, s.repo.RelativePath)
			_, _ = sendWorktreeDaemonCmd[worktreeDeleteDaemonResponse](
				ctx, a.daemonRouter, chat.UserID, "worktree.delete_directory",
				worktreeDeleteDaemonRequest{
					ProjectPath:  repoPath,
					WorktreePath: s.worktreePath,
				}, 30_000,
			)
		}
		return reason
	}

	var workspaceRoot string
	for _, repo := range repos {
		repoPath := filepath.Join(project.Path, repo.RelativePath)

		if force {
			// Stale-branch cleanup; the workspace dir itself is fresh per UUID.
			_, _ = sendWorktreeDaemonCmd[map[string]any](
				ctx, a.daemonRouter, chat.UserID, "worktree.force_cleanup",
				map[string]string{
					"project_path":  repoPath,
					"worktree_path": "",
					"branch":        branch,
				}, 30_000,
			)
		}

		createResp, err := sendWorktreeDaemonCmd[worktreeCreateDaemonResponse](
			ctx, a.daemonRouter, chat.UserID, "worktree.create",
			worktreeCreateDaemonRequest{
				ProjectPath: repoPath,
				WorkspaceID: workspaceID,
				SubPath:     repo.RelativePath,
				Name:        name,
				Branch:      branch,
				BaseBranch:  baseBranch,
				Force:       force,
				CopyFiles:   copyFiles,
			}, worktreeCreateDaemonTimeoutMs,
		)
		if err != nil {
			logging.Error("Failed to create git worktree via daemon", "error", err, "repo", repo.ID)
			return CreateWorktreeOutput{}, rollback(fmt.Errorf("failed to create worktree for repo %s: %w", repo.Name, err))
		}
		if !createResp.Success {
			logging.Error("Failed to create git worktree", "error", createResp.Error, "repo", repo.ID)
			return CreateWorktreeOutput{}, rollback(fmt.Errorf("failed to create worktree for repo %s: %s", repo.Name, createResp.Error))
		}

		// First successful create gives us the absolute workspace root.
		// daemon returns <HOME>/.reliant/worktrees/<workspace_id>[/<repo.rel>];
		// strip the trailing repo.RelativePath to get the workspace root.
		if workspaceRoot == "" {
			if repo.RelativePath == "" {
				workspaceRoot = createResp.WorktreePath
			} else {
				workspaceRoot = strings.TrimSuffix(createResp.WorktreePath,
					string(filepath.Separator)+repo.RelativePath)
				if workspaceRoot == createResp.WorktreePath {
					workspaceRoot = filepath.Dir(createResp.WorktreePath)
				}
			}
		}

		successes = append(successes, repoCreateResult{
			repo:         repo,
			worktreePath: createResp.WorktreePath,
			baseBranch:   firstNonEmptyWT(createResp.BaseBranch, baseBranch),
		})
	}

	// Persist one Worktree row representing the workspace. BaseBranch is the
	// resolved value of the first repo for display; BaseBranches captures
	// per-repo bases so PR creation (and other future write ops) can pick
	// the right base for each nested repo.
	displayBase := baseBranch
	if len(successes) > 0 {
		displayBase = successes[0].baseBranch
	}
	baseBranches := make(map[string]string, len(successes))
	for _, s := range successes {
		if s.baseBranch != "" {
			baseBranches[s.repo.ID] = s.baseBranch
		}
	}
	if len(baseBranches) <= 1 {
		// Single-repo: legacy BaseBranch alone is canonical, leave the map nil
		// so the JSON column stays NULL.
		baseBranches = nil
	}

	worktreeID := uuid.New().String()
	now := time.Now().UTC()
	var chatIDPtr *string
	if rtx.ChatID != "" {
		c := rtx.ChatID
		chatIDPtr = &c
	}
	wt := &db.Worktree{
		ID:           worktreeID,
		Name:         name,
		Path:         workspaceRoot,
		Branch:       branch,
		BaseBranch:   displayBase,
		BaseBranches: baseBranches,
		ProjectID:    project.ID,
		ChatID:       chatIDPtr,
		Status:       int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActive:   now,
	}
	if err := a.repo.CreateWorktree(ctx, wt); err != nil {
		logging.Error("Failed to create worktree row", "error", err)
		_ = rollback(nil)
		return CreateWorktreeOutput{}, fmt.Errorf("failed to persist worktree row: %w", err)
	}

	return CreateWorktreeOutput{
		Id:         worktreeID,
		Name:       name,
		Path:       workspaceRoot,
		Branch:     branch,
		BaseBranch: displayBase,
		// RepoId is intentionally empty: in the multi-repo model a worktree
		// is workspace-level, not tied to a single nested repo. Kept on the
		// proto for backwards-compat with existing workflow YAML fixtures.
		RepoId: "",
		Status: "active",
	}, nil
}

// firstNonEmptyWT returns the first non-empty string from values.
func firstNonEmptyWT(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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

// Execute tears down a workspace-level worktree. In multi-repo mode this means
// fanning out one daemon `worktree.delete_directory` per nested repo (each
// `git worktree remove`s the corresponding checkout from its parent repo) and
// then asking the daemon to remove the workspace root itself. In single-repo
// legacy projects (one Repo with empty RelativePath) it collapses to one
// daemon call against the workspace path. After daemon-side cleanup the
// Worktree row is soft-deleted.
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

	var worktree *db.Worktree
	for _, wt := range worktrees {
		if wt.Name == input.Name {
			worktree = wt
			break
		}
	}
	if worktree == nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("worktree '%s' not found", input.Name)
	}

	// Enumerate the project's nested repos. A standalone-repo project has
	// exactly one Repo with RelativePath == "" — that legacy single-repo
	// case collapses to the original single delete_directory call (the
	// workspace path is itself the git checkout).
	repos, err := a.repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return DeleteWorktreeOutput{}, fmt.Errorf("failed to list repos for project: %w", err)
	}

	legacySingleRepo := len(repos) <= 1 &&
		(len(repos) == 0 || repos[0].RelativePath == "")

	if legacySingleRepo {
		deleteResp, err := sendWorktreeDaemonCmd[worktreeDeleteDaemonResponse](
			ctx, a.daemonRouter, chat.UserID, "worktree.delete_directory",
			worktreeDeleteDaemonRequest{
				ProjectPath:  project.Path,
				WorktreePath: worktree.Path,
			}, 30_000,
		)
		if err != nil {
			return DeleteWorktreeOutput{}, fmt.Errorf("failed to delete worktree via daemon: %w", err)
		}
		if err := a.softDeleteWorktree(ctx, worktree.ID); err != nil {
			return DeleteWorktreeOutput{}, err
		}
		return DeleteWorktreeOutput{Deleted: deleteResp.Deleted}, nil
	}

	// Multi-repo: fan out per-repo cleanup. Best-effort — log and continue
	// on individual failures. A leaked git worktree registration is recoverable
	// (`git worktree prune`); blocking the whole teardown on one repo is not.
	for _, repo := range repos {
		repoPath := filepath.Join(project.Path, repo.RelativePath)
		checkoutPath := filepath.Join(worktree.Path, repo.RelativePath)
		resp, err := sendWorktreeDaemonCmd[worktreeDeleteDaemonResponse](
			ctx, a.daemonRouter, chat.UserID, "worktree.delete_directory",
			worktreeDeleteDaemonRequest{
				ProjectPath:  repoPath,
				WorktreePath: checkoutPath,
			}, 30_000,
		)
		if err != nil {
			logging.Warn("Per-repo worktree delete failed (continuing)",
				"repo", repo.ID, "checkout", checkoutPath, "error", err)
			continue
		}
		if !resp.Deleted {
			logging.Warn("Per-repo worktree delete reported not deleted (continuing)",
				"repo", repo.ID, "checkout", checkoutPath)
		}
	}

	// Wipe the workspace root itself (parent of the per-repo checkouts).
	wsResp, err := sendWorktreeDaemonCmd[worktreeRemoveWorkspaceDaemonResponse](
		ctx, a.daemonRouter, chat.UserID, "worktree.remove_workspace_dir",
		worktreeRemoveWorkspaceDaemonRequest{WorkspacePath: worktree.Path}, 30_000,
	)
	if err != nil {
		logging.Warn("Workspace dir removal failed (continuing)",
			"workspace", worktree.Path, "error", err)
	} else if !wsResp.Deleted && wsResp.Error != "" {
		logging.Warn("Workspace dir removal reported error (continuing)",
			"workspace", worktree.Path, "error", wsResp.Error)
	}

	if err := a.softDeleteWorktree(ctx, worktree.ID); err != nil {
		return DeleteWorktreeOutput{}, err
	}
	return DeleteWorktreeOutput{Deleted: true}, nil
}

// softDeleteWorktree marks the Worktree row as archived (DeletedAt set).
func (a *DeleteWorktreeActivity) softDeleteWorktree(ctx context.Context, worktreeID string) error {
	if err := a.repo.ArchiveWorktree(ctx, worktreeID); err != nil {
		return fmt.Errorf("failed to soft-delete worktree row: %w", err)
	}
	return nil
}

// ============================================================================
// DAEMON COMMAND TYPES & HELPERS
// ============================================================================

type worktreeCreateDaemonRequest struct {
	ProjectPath string   `json:"project_path"`
	WorkspaceID string   `json:"workspace_id"`
	SubPath     string   `json:"sub_path"`
	Name        string   `json:"name"`
	Branch      string   `json:"branch"`
	BaseBranch  string   `json:"base_branch"`
	Force       bool     `json:"force"`
	CopyFiles   []string `json:"copy_files,omitempty"`
}

type worktreeCreateDaemonResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	Error        string `json:"error,omitempty"`
}

type worktreeDeleteDaemonRequest struct {
	ProjectPath  string `json:"project_path"`
	WorktreePath string `json:"worktree_path"`
}

type worktreeDeleteDaemonResponse struct {
	Deleted bool `json:"deleted"`
}

type worktreeRemoveWorkspaceDaemonRequest struct {
	WorkspacePath string `json:"workspace_path"`
}

type worktreeRemoveWorkspaceDaemonResponse struct {
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
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
