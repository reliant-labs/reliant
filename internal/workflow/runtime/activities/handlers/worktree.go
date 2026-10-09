// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
	"go.temporal.io/sdk/temporal"
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
// This activity creates a new git worktree for a chat's project on the
// machine the run's tools execute on, via DaemonRouter.
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
	// Launch refuses a no-machine run of a workflow with this node; a git
	// checkout has nowhere to live without one.
	if chat.NoMachine {
		return CreateWorktreeOutput{}, temporal.NewNonRetryableApplicationError(
			nomachine.ErrNoMachine.Error()+": create_worktree needs a git checkout on the user's machine",
			"NoMachine", nil)
	}

	// Get project to access working directory
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to get project: %w", err)
	}

	// The worktree goes on the machine this run's tools execute on, chosen
	// exactly as ExecuteTools chooses it (toolDaemonSelector): the run's own
	// daemon (workflow-level, or the chat's pinned session daemon) over the
	// owner of the chat's worktree. The run's next steps work inside the new
	// worktree, so anywhere else and they cannot see it.
	//
	// Resolved ONCE: every per-repo create, any rollback, and the owner
	// recorded on the row must name the same machine. Without the recorded
	// owner, a chat later bound to this worktree routes its tools by default
	// resolution, which need not be where the checkout is.
	chatWorktreeOwner, err := worktreeOwnerOf(ctx, a.repo, chat.WorktreeID)
	if err != nil {
		return CreateWorktreeOutput{}, err
	}
	ownerDaemonID, err := toolexec.ResolveDaemonIDForSelector(ctx, a.daemonRouter, chat.UserID,
		toolDaemonSelector(chatWorktreeOwner, rtx.DaemonSelector))
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to resolve daemon for worktree: %w", err)
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
	// Exact paths relative to the workspace root, validated before any
	// checkout so a bad entry fails the node instead of a rollback.
	copyPaths, err := copypath.CleanAll(protoArgs.GetCopyFiles())
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("invalid copy_files: %w", err)
	}

	// workspacecreate.Insert defaults an empty branch to worktree/<name>-<unix>.
	var chatIDPtr *string
	if rtx.ChatID != "" {
		c := rtx.ChatID
		chatIDPtr = &c
	}
	createReq := workspacecreate.Request{
		Project:       project,
		Repos:         repos,
		Name:          name,
		Branch:        branch,
		BaseBranch:    baseBranch,
		Force:         force,
		CopyPaths:     copyPaths,
		ChatID:        chatIDPtr,
		OwnerDaemonID: ownerDaemonID,
	}
	wt, err := workspacecreate.Insert(ctx, a.repo, createReq)
	if err != nil {
		return CreateWorktreeOutput{}, fmt.Errorf("failed to persist worktree row: %w", err)
	}
	machine := daemonMachine{router: a.daemonRouter, userID: chat.UserID, daemonID: ownerDaemonID}
	finishErr := workspacecreate.Finish(ctx, a.repo, machine, createReq, wt)
	// Settled either way (ACTIVE or FAILED): tell connected clients.
	if err := a.repo.EmitUserRefetch(ctx, chat.UserID, db.RefetchWorktreeChanges, db.RefetchOpts{
		ProjectID:  &project.ID,
		WorktreeID: &wt.ID,
	}); err != nil {
		logging.Error("Failed to emit worktree refetch", "error", err, "worktreeID", wt.ID)
	}
	if finishErr != nil {
		return CreateWorktreeOutput{}, finishErr
	}

	return CreateWorktreeOutput{
		Id:         wt.ID,
		Name:       name,
		Path:       wt.Path,
		Branch:     wt.Branch,
		BaseBranch: wt.BaseBranch,
		// RepoId is intentionally empty: in the multi-repo model a worktree
		// is workspace-level, not tied to a single nested repo. Kept on the
		// proto for backwards-compat with existing workflow YAML fixtures.
		RepoId: "",
		Status: "active",
	}, nil
}

// daemonMachine is a workspacecreate.Machine bound to one owner daemon.
type daemonMachine struct {
	router   toolexec.DaemonRouter
	userID   string
	daemonID string
}

func (m daemonMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := m.router.SendDaemonCommandToDaemon(ctx, m.userID, m.daemonID, commandType, payloadBytes, timeoutMs)
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
	// The checkouts exist only on the worktree's owner; tearing them down
	// anywhere else deletes nothing and strands them there. A row with no
	// recorded owner keeps default resolution.
	ownerDaemonID := ""
	if worktree.DaemonID != nil {
		ownerDaemonID = *worktree.DaemonID
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
			ctx, a.daemonRouter, chat.UserID, ownerDaemonID, "worktree.delete_directory",
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
			ctx, a.daemonRouter, chat.UserID, ownerDaemonID, "worktree.delete_directory",
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
		ctx, a.daemonRouter, chat.UserID, ownerDaemonID, "worktree.remove_workspace_dir",
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

// worktreeOwnerOf returns the daemon owning a worktree's checkout, or "" when
// worktreeID is unset, names no row, or the row records no owner — the cases in
// which ExecuteTools routes by default resolution. Any other lookup failure is
// returned rather than read as "no owner".
func worktreeOwnerOf(ctx context.Context, repo db.Repository, worktreeID *string) (string, error) {
	if worktreeID == nil || *worktreeID == "" {
		return "", nil
	}
	worktree, err := repo.GetWorktree(ctx, *worktreeID)
	if errors.Is(err, core.ErrWorktreeNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get worktree %s: %w", *worktreeID, err)
	}
	if worktree.DaemonID == nil {
		return "", nil
	}
	return *worktree.DaemonID, nil
}

// sendWorktreeDaemonCmd marshals a request, sends it to daemonID ("" = default
// resolution), and unmarshals the response.
func sendWorktreeDaemonCmd[T any](ctx context.Context, router toolexec.DaemonRouter, userID, daemonID, commandType string, payload interface{}, timeoutMs int32) (T, error) {
	var zero T
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return zero, fmt.Errorf("marshal payload: %w", err)
	}
	var respBytes []byte
	if daemonID == "" {
		respBytes, err = router.SendDaemonCommand(ctx, userID, commandType, payloadBytes, timeoutMs)
	} else {
		respBytes, err = router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payloadBytes, timeoutMs)
	}
	if err != nil {
		return zero, fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	var resp T
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return zero, fmt.Errorf("unmarshal response for %s: %w", commandType, err)
	}
	return resp, nil
}
