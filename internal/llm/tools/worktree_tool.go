// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/rctx"
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

	// Exact paths to copy from the source repo — never searched for.
	CopyFiles []string `json:"copy_files,omitempty" jsonschema:"description=Exact paths relative to the repository root to copy into the new worktree (e.g. .env or web/node_modules). Each path is copied as-is; nothing is searched for."`

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
	Worktrees   []*WorktreeInfo        `json:"worktrees,omitempty"`
	StoredInCEL map[string]interface{} `json:"stored_in_cel,omitempty"` // Data stored in CEL context
}

// WorktreeStatus is a worktree's lifecycle state as the tool reports it.
type WorktreeStatus string

const (
	WorktreeStatusActive    WorktreeStatus = "active"
	WorktreeStatusCompleted WorktreeStatus = "completed"
	WorktreeStatusAbandoned WorktreeStatus = "abandoned"
)

// WorktreeInfo describes one worktree in the tool's response metadata.
type WorktreeInfo struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	WorkingDir string         `json:"working_dir"`
	Branch     string         `json:"branch"`
	BaseBranch string         `json:"base_branch"`
	RepoID     string         `json:"repo_id"`
	ProjectID  string         `json:"project_id"`
	SessionID  string         `json:"session_id"`
	Status     WorktreeStatus `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	LastActive time.Time      `json:"last_active"`
}

// RunMachine reaches the machine a run's tools execute on.
//
// The worktree tool runs on the worker, which has no access to the user's
// files: only the daemon does. So every git and filesystem step it takes is a
// daemon command, sent to the machine this run's tools execute on, which is
// also the machine recorded as the worktree's owner.
//
// Injected because the daemon router lives in toolexec, which imports this
// package. Optional: without it the tool reports that creating and deleting
// worktrees is unavailable here, rather than touching a local disk.
type RunMachine interface {
	// DaemonID returns the daemon this run's tools execute on, chosen as
	// ExecuteTools chose it: the run's daemon selector travels on ctx.
	DaemonID(ctx context.Context, userID string) (string, error)
	// Send delivers one daemon command to daemonID and returns its reply.
	Send(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

const (
	// worktreeCheckoutTimeoutMs bounds commands whose cost grows with the
	// repository: the checkout itself and copying copy_files into it.
	worktreeCheckoutTimeoutMs int32 = 120_000
	worktreeCommandTimeoutMs  int32 = 30_000
)

type worktreeTool struct {
	repo    db.Repository
	machine RunMachine
}

const WorktreeToolName = "worktree"

func NewWorktreeTool(repo db.Repository, machine RunMachine) Tool {
	tool := &worktreeTool{repo: repo, machine: machine}
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
- copy_files: exact paths relative to the repository root, for gitignored files a fresh checkout lacks
- Nothing is searched for: ".env" copies only the root .env; name "frontend/.env" to copy that one
- A directory is copied whole (e.g. "web/node_modules"); a missing path is skipped

EXAMPLES:

Create a worktree that carries over local env files:
{
  "action": "create",
  "name": "feature-auth",
  "base_branch": "main",
  "copy_files": [".env", "frontend/.env.local"]
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
	// The run's context carries the user and the daemon selector the
	// machine is resolved from.
	ctx := context.Background()
	if rctx != nil && rctx.Context != nil {
		ctx = rctx.Context
	}

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
	if err := validateWorktreeName(p.Name); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}
	// Exact paths, validated before any git work so a bad entry is an error
	// rather than a worktree that silently lacks its .env.
	copyPaths, err := copypath.CleanAll(p.CopyFiles)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: invalid copy_files: %v", err)), nil
	}

	// Get working directory for current repository
	workingDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get working directory: %v", err)), nil
	}

	// Resolved once: every command below, and the owner recorded on the row,
	// must name the same machine.
	machine, err := w.runMachine(ctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}
	loc, err := machine.locate(ctx, workingDir, p.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}

	// The registry is the project's worktree rows, which hold one row per
	// name, archived ones included.
	projectID := ""
	if rctx != nil && rctx.Project != nil {
		projectID = rctx.Project.ID
	}
	registered, err := w.registered(ctx, projectID, p.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}
	if registered != nil {
		if refusal := createRefusal(registered, loc.path, machine.daemonID, p); refusal != "" {
			return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %s", refusal)), nil
		}
	}

	if p.Force {
		// Clear whatever holds this name on the machine: the checkout at
		// its path, stale git registrations, and the named branch.
		if _, err := sendWorktreeCommand[map[string]any](ctx, machine, machine.daemonID, "worktree.force_cleanup",
			map[string]string{"project_path": workingDir, "worktree_path": loc.path, "branch": p.Branch},
			worktreeCommandTimeoutMs); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
		}
	}

	// The daemon does not report the branch it picked, so pick it here.
	branch := p.Branch
	if branch == "" {
		branch = fmt.Sprintf("worktree/%s-%d", p.Name, time.Now().Unix())
	}

	created, err := sendWorktreeCommand[worktreeToolCreateResponse](ctx, machine, machine.daemonID, "worktree.create",
		worktreeToolCreateRequest{
			ProjectPath: workingDir,
			RepoID:      loc.repoID,
			Name:        p.Name,
			Branch:      branch,
			BaseBranch:  p.BaseBranch,
			Force:       p.Force,
		}, worktreeCheckoutTimeoutMs)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", err)), nil
	}
	if !created.Success {
		return NewTextErrorResponse(fmt.Sprintf("Failed to create worktree: %v", gitWorktreeAddError(created.Error, loc.path, branch, p.BaseBranch))), nil
	}
	path := created.WorktreePath
	if path != loc.path {
		// The registry key and the checkout must agree; say so if they
		// do not rather than silently keying the row differently.
		logging.Warn("worktree.create used a different path than expected", "expected", loc.path, "actual", path)
	}

	// Copy specified files from the source repo into the worktree. A path
	// that fails to copy is logged, not fatal: the checkout is complete.
	if len(copyPaths) > 0 {
		copied, err := sendWorktreeCommand[worktreeToolCopyPathsResponse](ctx, machine, machine.daemonID, "worktree.copy_paths",
			map[string]any{"source_root": workingDir, "dest_root": path, "paths": copyPaths},
			worktreeCheckoutTimeoutMs)
		switch {
		case err != nil:
			logging.Warn("Failed to copy paths into worktree", "path", path, "error", err)
		case copied.Error != "":
			logging.Warn("Failed to copy paths into worktree", "path", path, "error", copied.Error)
		default:
			for entry, reason := range copied.Failed {
				logging.Warn("Failed to copy path into worktree", "path", entry, "error", reason)
			}
		}
	}

	sessionID := p.SessionID
	if sessionID == "" && rctx != nil {
		sessionID = rctx.ChatID
	}
	baseBranch := created.BaseBranch
	if baseBranch == "" {
		baseBranch = p.BaseBranch
	}
	wt := &WorktreeInfo{
		ID:         fmt.Sprintf("%s/%s", loc.repoID, p.Name),
		Name:       p.Name,
		Path:       path,
		Branch:     branch,
		BaseBranch: baseBranch,
		RepoID:     loc.repoID,
		SessionID:  sessionID,
		Status:     WorktreeStatusActive,
	}

	// Also persist to the database so the UI and List/Get can find it.
	// Failing to is logged, not fatal: the checkout was already created.
	if w.repo != nil && rctx != nil && rctx.Project != nil {
		if err := w.persist(ctx, registered, wt, rctx, machine.daemonID); err != nil {
			logging.Warn("Failed to persist worktree to database", "worktreeID", wt.ID, "error", err)
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

	// Convert to WorktreeInfo for response metadata
	worktrees := make([]*WorktreeInfo, len(dbWorktrees))
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
	if err := validateWorktreeName(p.Name); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}

	// Get working directory for current repository
	workingDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get working directory: %v", err)), nil
	}

	machine, err := w.runMachine(ctx)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}
	loc, err := machine.locate(ctx, workingDir, p.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}

	// Only a live worktree this tool created under that name is deleted. A
	// workspace made elsewhere that shares the name lives at another path
	// and is not touched.
	projectID := ""
	if rctx != nil && rctx.Project != nil {
		projectID = rctx.Project.ID
	}
	registered, err := w.registered(ctx, projectID, p.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}
	if registered == nil || registered.DeletedAt != nil || registered.Path != loc.path {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: worktree '%s' not found", p.Name)), nil
	}

	// The checkout exists only on the worktree's owner. A row that records
	// none was made on the run's machine, the only one this tool uses.
	owner := machine.daemonID
	if registered.DaemonID != nil && *registered.DaemonID != "" {
		owner = *registered.DaemonID
	}
	deleted, err := sendWorktreeCommand[worktreeToolDeleteResponse](ctx, machine, owner, "worktree.delete_directory",
		map[string]string{"project_path": workingDir, "worktree_path": registered.Path},
		worktreeCommandTimeoutMs)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete worktree: %v", err)), nil
	}
	if !deleted.Deleted {
		logging.Warn("failed to remove worktree directory", "path", registered.Path, "daemonID", owner)
	}

	if err := w.repo.ArchiveWorktree(ctx, registered.ID); err != nil {
		logging.Warn("Failed to archive deleted worktree", "worktreeID", registered.ID, "error", err)
	}

	metadata := WorktreeResponseMetadata{
		Action: "delete",
	}

	content := fmt.Sprintf("Successfully deleted worktree: %s", p.Name)
	return WithResponseMetadata(NewTextResponse(content), metadata), nil
}

// worktreeRunMachine is the machine one call of the tool works on.
type worktreeRunMachine struct {
	RunMachine
	userID   string
	daemonID string
}

// runMachine resolves the machine this run's tools execute on.
func (w *worktreeTool) runMachine(ctx context.Context) (*worktreeRunMachine, error) {
	if w.machine == nil {
		return nil, errors.New("no machine can be reached from here to run git")
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, errors.New("no user in the tool context")
	}
	daemonID, err := w.machine.DaemonID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve the run's machine: %w", err)
	}
	return &worktreeRunMachine{RunMachine: w.machine, userID: userID, daemonID: daemonID}, nil
}

// worktreeLocation is where a named worktree of a repository lives on the
// run's machine.
type worktreeLocation struct {
	repoID string
	path   string
}

// locate returns where worktree.create puts a worktree called name for the
// repository at workingDir: <HOME>/.reliant/worktrees/<repo_id>/<name>, the
// layout it uses when given no workspace id (daemonruntime
// handleWorktreeCreate). The tool needs the path BEFORE creating: it is the
// key the name is registered under, and what force=true clears. Both parts
// come from the machine itself, because its home and the repository's remote
// are visible only there.
func (m *worktreeRunMachine) locate(ctx context.Context, workingDir, name string) (worktreeLocation, error) {
	repo, err := sendWorktreeCommand[struct {
		RepoID string `json:"repo_id"`
	}](ctx, m, m.daemonID, "worktree.generate_repo_id", map[string]string{"project_path": workingDir}, worktreeCommandTimeoutMs)
	if err != nil {
		return worktreeLocation{}, err
	}
	if repo.RepoID == "" {
		return worktreeLocation{}, errors.New("the machine returned no repository id")
	}
	home, err := sendWorktreeCommand[struct {
		HomeDir string `json:"home_dir"`
		Error   string `json:"error,omitempty"`
	}](ctx, m, m.daemonID, "skills.get_home_dir", struct{}{}, worktreeCommandTimeoutMs)
	if err != nil {
		return worktreeLocation{}, err
	}
	if home.HomeDir == "" {
		return worktreeLocation{}, fmt.Errorf("the machine has no home directory: %s", home.Error)
	}
	return worktreeLocation{
		repoID: repo.RepoID,
		path:   ospath.Join(home.HomeDir, ".reliant", "worktrees", repo.RepoID, name),
	}, nil
}

// registered returns the project's worktree row called name, archived or
// not, or nil. A project holds one row per name.
func (w *worktreeTool) registered(ctx context.Context, projectID, name string) (*db.Worktree, error) {
	if w.repo == nil || projectID == "" {
		return nil, nil
	}
	const page = 100
	for offset := 0; ; offset += page {
		rows, err := w.repo.ListWorktrees(ctx, db.WorktreeFilters{
			ProjectID: &projectID, IncludeArchived: true, Limit: page, Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("look up existing worktrees: %w", err)
		}
		for _, row := range rows {
			if row.Name == name {
				return row, nil
			}
		}
		if len(rows) < page {
			return nil, nil
		}
	}
}

// createRefusal says why the name cannot be (re)created, given the row that
// already holds it, or "" when it can.
//
// Only a row this tool made for the name on this machine is reused: one at
// the tool's own path whose checkout is here. force=true wipes that path, so
// it must never be applied to a workspace made some other way, which lives at
// another path and may be in use.
func createRefusal(existing *db.Worktree, path, daemonID string, p *WorktreeParams) string {
	switch {
	case existing.Path != path:
		return fmt.Sprintf("worktree '%s' already exists in Reliant registry as another workspace (use a different name)", p.Name)
	case existing.DaemonID != nil && *existing.DaemonID != "" && *existing.DaemonID != daemonID:
		return fmt.Sprintf("worktree '%s' already exists in Reliant registry on another machine (use a different name)", p.Name)
	case existing.DeletedAt == nil && !p.Force:
		return fmt.Sprintf("worktree '%s' already exists in Reliant registry (use force=true to override)", p.Name)
	}
	return ""
}

// persist records the created worktree. A row the tool made for the name
// before (deleted, or replaced by force=true) is brought back in place, so
// chats bound to it stay bound; otherwise a new row records the machine as
// the owner, so a chat later bound to the worktree, and every operation on
// it, routes to the machine that has the checkout.
func (w *worktreeTool) persist(ctx context.Context, existing *db.Worktree, wt *WorktreeInfo, rc *rctx.ToolContext, daemonID string) error {
	now := time.Now().UTC()
	if existing != nil {
		existing.Branch = wt.Branch
		existing.BaseBranch = wt.BaseBranch
		existing.Path = wt.Path
		existing.Status = 1 // WORKTREE_STATUS_ACTIVE
		existing.LastActive = now
		if err := w.repo.UpdateWorktree(ctx, existing); err != nil {
			return err
		}
		if existing.DeletedAt != nil {
			return w.repo.UnarchiveWorktree(ctx, existing.ID)
		}
		return nil
	}
	var chatID *string
	if rc.ChatID != "" {
		chatID = &rc.ChatID
	}
	return w.repo.CreateWorktree(ctx, &db.Worktree{
		ID:         uuid.New().String(),
		Name:       wt.Name,
		Path:       wt.Path,
		Branch:     wt.Branch,
		BaseBranch: wt.BaseBranch,
		ProjectID:  rc.Project.ID,
		ChatID:     chatID,
		DaemonID:   &daemonID,
		Status:     1, // WORKTREE_STATUS_ACTIVE
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
}

// validateWorktreeName refuses a name that is not one plain path segment. The
// name becomes the last component of a directory on the user's machine, which
// force=true wipes and delete removes: "../.." would aim both outside
// ~/.reliant/worktrees.
func validateWorktreeName(name string) error {
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid worktree name %q: use a single directory name, without slashes", name)
	}
	return nil
}

// gitWorktreeAddError turns the output of a failed `git worktree add` into the
// error the tool reports.
func gitWorktreeAddError(output, path, branch, baseBranch string) error {
	switch {
	case strings.Contains(output, "already exists"):
		return fmt.Errorf("worktree path '%s' already exists", path)
	case strings.Contains(output, "already checked out"):
		return fmt.Errorf("branch '%s' is already checked out in another worktree", branch)
	case strings.Contains(output, "not a valid branch"):
		return fmt.Errorf("base branch '%s' is not a valid branch", baseBranch)
	default:
		return fmt.Errorf("git worktree creation failed: %s", strings.TrimSpace(output))
	}
}

type worktreeToolCreateRequest struct {
	ProjectPath string `json:"project_path"`
	RepoID      string `json:"repo_id"`
	Name        string `json:"name"`
	Branch      string `json:"branch"`
	BaseBranch  string `json:"base_branch"`
	Force       bool   `json:"force"`
}

type worktreeToolCreateResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	Error        string `json:"error,omitempty"`
}

type worktreeToolCopyPathsResponse struct {
	Failed map[string]string `json:"failed,omitempty"`
	Error  string            `json:"error,omitempty"`
}

type worktreeToolDeleteResponse struct {
	Deleted bool `json:"deleted"`
}

// sendWorktreeCommand marshals payload, sends it to daemonID and decodes the
// reply into T.
func sendWorktreeCommand[T any](ctx context.Context, m *worktreeRunMachine, daemonID, commandType string, payload any, timeoutMs int32) (T, error) {
	var resp T
	body, err := json.Marshal(payload)
	if err != nil {
		return resp, fmt.Errorf("%s: marshal request: %w", commandType, err)
	}
	raw, err := m.Send(ctx, m.userID, daemonID, commandType, body, timeoutMs)
	if err != nil {
		return resp, fmt.Errorf("%s: %w", commandType, err)
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return resp, fmt.Errorf("%s: decode reply: %w", commandType, err)
	}
	return resp, nil
}

// dbWorktreeToWorktree converts a db.Worktree to a WorktreeInfo for response metadata
func dbWorktreeToWorktree(dbWt *db.Worktree) *WorktreeInfo {
	status := WorktreeStatusActive
	switch dbWt.Status {
	case 1:
		status = WorktreeStatusActive
	case 2:
		status = WorktreeStatusCompleted
	case 3:
		status = WorktreeStatusAbandoned
	}

	sessionID := ""
	if dbWt.ChatID != nil {
		sessionID = *dbWt.ChatID
	}

	return &WorktreeInfo{
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
