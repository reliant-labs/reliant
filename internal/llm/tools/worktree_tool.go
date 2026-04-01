// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/worktree"
)

// WorktreeParams defines the parameters for worktree operations
type WorktreeParams struct {
	// Action to perform: "create", "list", "get", "delete"
	Action string `json:"action" jsonschema:"required,enum=create,enum=list,enum=get,enum=delete,description=Action to perform on worktree"`

	// Name of the worktree (required for create, get, delete)
	Name string `json:"name,omitempty" jsonschema:"description=Name of the worktree"`

	// Branch name for create action (optional, auto-generated if not provided)
	Branch string `json:"branch,omitempty" jsonschema:"description=Branch name to use (auto-generated if not specified)"`

	// Base branch to branch from (optional, defaults to main/master)
	BaseBranch string `json:"base_branch,omitempty" jsonschema:"description=Base branch to branch from (defaults to repository default branch)"`

	// Files to copy from source repo (e.g., .env, .env.local)
	CopyFiles []string `json:"copy_files,omitempty" jsonschema:"description=Files to copy from source repository (searches recursively in all directories)"`

	// Force creation by deleting existing worktree/branch
	Force bool `json:"force,omitempty" jsonschema:"description=Force creation by deleting existing worktree and branch if they exist"`

	// SessionID to associate with the worktree
	SessionID string `json:"session_id,omitempty" jsonschema:"description=Session ID to associate with worktree"`
}

// WorktreeResponseMetadata contains metadata about the worktree operation
type WorktreeResponseMetadata struct {
	Action      string                 `json:"action"`
	WorktreeID  string                 `json:"worktree_id,omitempty"`
	Path        string                 `json:"path,omitempty"`
	Branch      string                 `json:"branch,omitempty"`
	Worktrees   []*worktree.Worktree   `json:"worktrees,omitempty"`
	StoredInCEL map[string]interface{} `json:"stored_in_cel,omitempty"` // Data stored in CEL context
}

// Current blockers:
//   - create/delete actions use worktree.NewService() which internally calls exec.Command("git", ...)
//     for git worktree add/remove, branch creation, and file copying.
//   - To migrate: either rewrite worktree.Service to accept a daemon.Executor and use
//     daemon.RunCommand() for git operations, or shell out git commands through rctx.Daemon.RunCommand().
//   - list/get actions are DB-only (w.repo) and already work without daemon access.
//   - Risk: worktree.Service has complex error handling and rollback logic that would be
//     difficult to replicate via raw shell commands through the daemon.
type worktreeTool struct {
	repo db.Repository
}

const WorktreeToolName = "worktree"

func NewWorktreeTool(repo db.Repository) Tool {
	tool := &worktreeTool{repo: repo}
	return NewToolWrapper(tool)
}

func (w *worktreeTool) Name() string {
	return WorktreeToolName
}

func (w *worktreeTool) Description() string {
	return `Manage git worktrees for parallel development workflows.

WHEN TO USE:
- Creating isolated development environments for features/bugs
- Setting up parallel workspaces for agents
- Managing multiple concurrent work streams
ACTIONS:
1. create - Create a new git worktree
   Required: name
   Optional: branch, base_branch, copy_files, force, session_id

2. list - List all worktrees
   No parameters required

3. get - Get details of a specific worktree
   Required: name

4. delete - Delete a worktree
   Required: name

WORKTREE DATA STORAGE:
- Worktree information is automatically stored in CEL context as 'worktree_data'
- Available fields: id, name, path, branch, base_branch, repo_id
- Use in subsequent steps: worktree_data.path, worktree_data.branch, etc.

FILE COPYING:
- copy_files: Searches recursively for matching files (e.g., ".env" finds all .env files in any directory)
- Directory structure is preserved (frontend/.env -> worktree/frontend/.env)

EXAMPLES:

Create a worktree with recursive file copy:
{
  "action": "create",
  "name": "feature-auth",
  "base_branch": "main",
  "copy_files": [".env", ".env.local"]
}

List all worktrees:
{
  "action": "list"
}

NOTES:
- Worktree paths are stored in ~/.reliant/worktrees/<repo_id>/<name>
- Each worktree gets its own branch and working directory
- Use force=true to recreate existing worktrees
- Worktree data is stored globally for cleanup tracking`
}

func (w *worktreeTool) RequiresPermission(params WorktreeParams) (bool, error) {
	// Delete and create with force require permission
	if params.Action == "delete" || (params.Action == "create" && params.Force) {
		return true, nil
	}

	return false, nil
}

func (w *worktreeTool) Execute(rctx *rctx.ToolContext, params WorktreeParams) (ToolResponse, error) {
	ctx := context.Background()

	switch params.Action {
	case "create":
		return w.handleCreate(ctx, &params, rctx)
	case "list":
		return w.handleList(ctx, &params, rctx)
	case "get":
		return w.handleGet(ctx, &params, rctx)
	case "delete":
		return w.handleDelete(ctx, &params, rctx)
	default:
		return NewTextErrorResponse(fmt.Sprintf("Unknown action: %s", params.Action)), nil
	}
}

func (w *worktreeTool) handleCreate(ctx context.Context, p *WorktreeParams, rctx *rctx.ToolContext) (ToolResponse, error) {
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for create action"), nil
	}

	// Get working directory for current repository
	workingDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get working directory: %v", err)), nil
	}

	// Create worktree service for git operations
	svc, err := worktree.NewService("", workingDir)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree service: %v", err)), nil
	}

	opts := worktree.CreateOptions{
		Branch:     p.Branch,
		BaseBranch: p.BaseBranch,
		SessionID:  p.SessionID,
		CopyFiles:  p.CopyFiles,
		Force:      p.Force,
	}

	// If SessionID not provided, try to get from context
	if opts.SessionID == "" && rctx != nil {
		opts.SessionID = rctx.ChatID
	}

	wt, err := svc.Create(ctx, p.Name, opts)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}

	// Also persist to the database so the UI and List/Get can find it
	if w.repo != nil && rctx != nil && rctx.Project != nil {
		now := time.Now().UTC()
		var chatID *string
		if rctx.ChatID != "" {
			chatID = &rctx.ChatID
		}
		dbWorktree := &db.Worktree{
			ID:         uuid.New().String(),
			Name:       wt.Name,
			Path:       wt.Path,
			Branch:     wt.Branch,
			BaseBranch: wt.BaseBranch,
			ProjectID:  rctx.Project.ID,
			ChatID:     chatID,
			Status:     1, // WORKTREE_STATUS_ACTIVE
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}
		if err := w.repo.CreateWorktree(ctx, dbWorktree); err != nil {
			// Log but don't fail - the git worktree was already created successfully
			fmt.Printf("Warning: failed to persist worktree to database: %v\n", err)
		}
	}

	// Prepare data to store in CEL context
	celData := map[string]interface{}{
		"id":          wt.ID,
		"name":        wt.Name,
		"path":        wt.Path,
		"branch":      wt.Branch,
		"base_branch": wt.BaseBranch,
		"repo_id":     wt.RepoID,
		"session_id":  wt.SessionID,
		"status":      string(wt.Status),
	}

	metadata := WorktreeResponseMetadata{
		Action:      "create",
		WorktreeID:  wt.ID,
		Path:        wt.Path,
		Branch:      wt.Branch,
		StoredInCEL: celData,
	}

	content := fmt.Sprintf(`Created worktree successfully:
- Name: %s
- Path: %s
- Branch: %s
- Base Branch: %s
- ID: %s

Worktree data stored in CEL context as 'worktree_data'
Access in workflows: worktree_data.path, worktree_data.branch, etc.`,
		wt.Name, wt.Path, wt.Branch, wt.BaseBranch, wt.ID)

	return WithResponseMetadata(NewTextResponse(content), metadata), nil
}

func (w *worktreeTool) handleList(ctx context.Context, p *WorktreeParams, rctx *rctx.ToolContext) (ToolResponse, error) {
	if w.repo == nil {
		return NewTextErrorResponse("Database not available for listing worktrees"), nil
	}

	filters := db.WorktreeFilters{
		Limit: 100,
	}

	// Scope to project if available
	if rctx != nil && rctx.Project != nil {
		filters.ProjectID = &rctx.Project.ID
	}

	dbWorktrees, err := w.repo.ListWorktrees(ctx, filters)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list worktrees: %v", err)), nil
	}

	if len(dbWorktrees) == 0 {
		return NewTextResponse("No worktrees found"), nil
	}

	// Convert to worktree.Worktree for response metadata
	worktrees := make([]*worktree.Worktree, len(dbWorktrees))
	content := fmt.Sprintf("Found %d worktrees:\n\n", len(dbWorktrees))
	for i, dbWt := range dbWorktrees {
		wt := dbWorktreeToWorktree(dbWt)
		worktrees[i] = wt
		content += fmt.Sprintf("%d. %s\n   Path: %s\n   Branch: %s\n   Status: %s\n   Main: %v\n",
			i+1, wt.Name, wt.Path, wt.Branch, wt.Status, dbWt.IsMain)
		if dbWt.ChatID != nil {
			content += fmt.Sprintf("   Chat: %s\n", *dbWt.ChatID)
		}
		content += "\n"
	}

	metadata := WorktreeResponseMetadata{
		Action:    "list",
		Worktrees: worktrees,
	}

	return WithResponseMetadata(NewTextResponse(content), metadata), nil
}

func (w *worktreeTool) handleGet(ctx context.Context, p *WorktreeParams, rctx *rctx.ToolContext) (ToolResponse, error) {
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for get action"), nil
	}

	if w.repo == nil {
		return NewTextErrorResponse("Database not available for getting worktree"), nil
	}

	// Find the worktree by name - list all worktrees for the project and filter by name
	filters := db.WorktreeFilters{
		Limit: 100,
	}
	if rctx != nil && rctx.Project != nil {
		filters.ProjectID = &rctx.Project.ID
	}

	dbWorktrees, err := w.repo.ListWorktrees(ctx, filters)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get worktree: %v", err)), nil
	}

	var dbWt *db.Worktree
	for _, wt := range dbWorktrees {
		if wt.Name == p.Name {
			dbWt = wt
			break
		}
	}

	if dbWt == nil {
		return NewTextErrorResponse(fmt.Sprintf("Worktree '%s' not found", p.Name)), nil
	}

	wt := dbWorktreeToWorktree(dbWt)

	// Prepare data to store in CEL context
	celData := map[string]interface{}{
		"id":          wt.ID,
		"name":        wt.Name,
		"path":        wt.Path,
		"branch":      wt.Branch,
		"base_branch": wt.BaseBranch,
		"project_id":  wt.ProjectID,
		"status":      string(wt.Status),
	}

	metadata := WorktreeResponseMetadata{
		Action:      "get",
		WorktreeID:  wt.ID,
		Path:        wt.Path,
		Branch:      wt.Branch,
		StoredInCEL: celData,
	}

	content := fmt.Sprintf(`Worktree: %s
- ID: %s
- Path: %s
- Branch: %s
- Base Branch: %s
- Status: %s
- Created: %s
- Last Active: %s`,
		wt.Name, wt.ID, wt.Path, wt.Branch, wt.BaseBranch,
		wt.Status, wt.CreatedAt.Format("2006-01-02 15:04:05"),
		wt.LastActive.Format("2006-01-02 15:04:05"))

	content += "\n\nWorktree data stored in CEL context as 'worktree_data'"

	return WithResponseMetadata(NewTextResponse(content), metadata), nil
}

func (w *worktreeTool) handleDelete(ctx context.Context, p *WorktreeParams, rctx *rctx.ToolContext) (ToolResponse, error) {
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for delete action"), nil
	}

	// Get working directory for current repository
	workingDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get working directory: %v", err)), nil
	}

	// Create worktree service for git operations
	svc, err := worktree.NewService("", workingDir)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree service: %v", err)), nil
	}

	if err := svc.Delete(ctx, p.Name); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}

	// Also remove from the database
	if w.repo != nil && rctx != nil && rctx.Project != nil {
		filters := db.WorktreeFilters{
			ProjectID: &rctx.Project.ID,
			Limit:     100,
		}
		dbWorktrees, err := w.repo.ListWorktrees(ctx, filters)
		if err == nil {
			for _, dbWt := range dbWorktrees {
				if dbWt.Name == p.Name {
					_ = w.repo.ArchiveWorktree(ctx, dbWt.ID)
					break
				}
			}
		}
	}

	metadata := WorktreeResponseMetadata{
		Action: "delete",
	}

	content := fmt.Sprintf("Successfully deleted worktree: %s", p.Name)
	return WithResponseMetadata(NewTextResponse(content), metadata), nil
}

// dbWorktreeToWorktree converts a db.Worktree to a worktree.Worktree for response metadata
func dbWorktreeToWorktree(dbWt *db.Worktree) *worktree.Worktree {
	status := worktree.StatusActive
	switch dbWt.Status {
	case 1:
		status = worktree.StatusActive
	case 2:
		status = worktree.StatusCompleted
	case 3:
		status = worktree.StatusAbandoned
	}

	sessionID := ""
	if dbWt.ChatID != nil {
		sessionID = *dbWt.ChatID
	}

	return &worktree.Worktree{
		ID:         dbWt.ID,
		Name:       dbWt.Name,
		Path:       dbWt.Path,
		Branch:     dbWt.Branch,
		BaseBranch: dbWt.BaseBranch,
		ProjectID:  dbWt.ProjectID,
		SessionID:  sessionID,
		Status:     status,
		CreatedAt:  dbWt.CreatedAt,
		UpdatedAt:  dbWt.UpdatedAt,
		LastActive: dbWt.LastActive,
	}
}

// IsReadOnly implements ReadOnlyTool
func (w *worktreeTool) IsReadOnly() bool {
	return false // Worktree operations modify file system
}
