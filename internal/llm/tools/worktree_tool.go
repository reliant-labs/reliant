// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/rctx"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
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
	CopyFiles []string `json:"copy_files,omitempty" jsonschema:"description=Exact paths relative to the project root to copy into the new worktree (e.g. .env or web/node_modules). Each path is copied as-is; nothing is searched for."`

	// Force creation by deleting existing worktree/branch
	Force bool `json:"force,omitempty" jsonschema:"description=Delete a stale branch of the same name in each repository before creating it"`
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
	WorktreeStatusMerging   WorktreeStatus = "merging"
	WorktreeStatusCreating  WorktreeStatus = "creating"
	WorktreeStatusFailed    WorktreeStatus = "failed"
)

// WorktreeInfo describes one worktree in the tool's response metadata. Path is
// the workspace root; Checkouts maps each repository of the project to its
// checkout inside it.
type WorktreeInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Checkouts  map[string]string `json:"checkouts,omitempty"`
	Branch     string            `json:"branch"`
	BaseBranch string            `json:"base_branch"`
	ProjectID  string            `json:"project_id"`
	SessionID  string            `json:"session_id"`
	DaemonID   string            `json:"daemon_id,omitempty"`
	Status     WorktreeStatus    `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	LastActive time.Time         `json:"last_active"`
}

// RunMachine reaches the machine a run's tools execute on.
//
// The worktree tool runs on the worker, which has no access to the user's
// files: only the daemon does. So every git and filesystem step it takes is a
// daemon command, sent to the machine this run's tools execute on, which is
// also the machine recorded as the worktree's owner.
//
// Injected because the daemon router lives in toolexec, which imports this
// package. Optional: without it the tool reports that creating worktrees is
// unavailable here, rather than touching a local disk.
type RunMachine interface {
	// DaemonID returns the daemon this run's tools execute on, chosen as
	// ExecuteTools chose it: the run's daemon selector travels on ctx.
	DaemonID(ctx context.Context, userID string) (string, error)
	// Send delivers one daemon command to daemonID and returns its reply.
	Send(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

const (
	worktreeListPage  = 100
	worktreeAdoptMs   = 30_000
	worktreeCreateMax = 10 * time.Minute
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
	return `Create and manage worktrees: isolated workspaces for working on a separate branch.

This is the ONLY way to make a worktree. Never run "git worktree add" yourself: the shell refuses it, because a checkout made by hand is invisible to Reliant and litters the project folder.

A worktree made here is the same workspace the "new worktree" button in the Reliant UI makes. It lives at ~/.reliant/worktrees/<project>/<name>-<id>/ and holds one checkout per repository of the project (laid out like the project itself), each on its own branch. It appears in the Reliant sidebar.

ACTIONS:
1. create - Required: name. Optional: branch, base_branch, copy_files, force
   Returns the worktree id, the workspace root path, the branch, and the path of each repository's checkout.
   The name must be unique among the project's live worktrees; an archived (deleted) worktree's name can be reused.

2. list - List the project's worktrees (any state: creating, active, failed)

3. get - Required: name. Details, paths and owner of one worktree

4. delete - Required: name. Archives the worktree; its directory is removed later, only when it is clean, pushed and unused. Refuses your own current worktree and any worktree a chat is using.

WORKING IN A WORKTREE:
- Pass its name to spawn: spawn(worktree=<name>) runs the sub-agent inside it, so its shell and file tools work on that branch.
- Or use the absolute paths from the result with commands that take a path (cd <path> && ...).
- Commit and push from inside the worktree; each repository's checkout is on its own branch.

FILE COPYING:
- copy_files: exact paths relative to the project root, for gitignored files a fresh checkout lacks (the new worktree mirrors the project's layout)
- Nothing is searched for: ".env" copies only the root .env; name "frontend/.env" to copy that one
- A directory is copied whole (e.g. "web/node_modules"); a missing path is skipped
- When you are working inside a worktree, files are copied from that worktree

WORKFLOW DATA:
- Result fields are stored in CEL context as 'worktree_data': id, name, path, branch, base_branch, project_id, status, session_id, checkouts

EXAMPLE:
{
  "action": "create",
  "name": "feature-auth",
  "base_branch": "main",
  "copy_files": [".env", "frontend/.env.local"]
}
then spawn a sub-agent in it with spawn(worktree="feature-auth").`
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
		return w.handleList(ctx, rctx)
	case "get":
		return w.handleGet(ctx, &params, rctx)
	case "delete":
		return w.handleDelete(ctx, &params, rctx)
	default:
		return NewTextErrorResponse(fmt.Sprintf("Unknown action: %s", params.Action)), nil
	}
}

func (w *worktreeTool) handleCreate(ctx context.Context, p *WorktreeParams, rc *rctx.ToolContext) (ToolResponse, error) {
	fail := func(format string, args ...any) (ToolResponse, error) {
		return NewTextErrorResponse("Failed to create worktree: " + fmt.Sprintf(format, args...)), nil
	}
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for create action"), nil
	}
	if err := validateWorktreeName(p.Name); err != nil {
		return fail("%v", err)
	}
	// Exact paths, validated before any git work so a bad entry is an error
	// rather than a worktree that silently lacks its .env.
	copyPaths, err := copypath.CleanAll(p.CopyFiles)
	if err != nil {
		return fail("invalid copy_files: %v", err)
	}
	project, err := w.project(rc)
	if err != nil {
		return fail("%v", err)
	}

	// Resolved once: every command below, and the owner recorded on the row,
	// must name the same machine.
	machine, err := w.runMachine(ctx)
	if err != nil {
		return fail("%v", err)
	}
	repos, err := w.projectRepos(ctx, rc, project, machine)
	if err != nil {
		return fail("%v", err)
	}

	var chatID *string
	if rc.ChatID != "" {
		id := rc.ChatID
		chatID = &id
	}
	// When working inside a worktree, copy_files come from it, so the new
	// workspace gets this one's .env and friends. The main checkout is the
	// default source.
	copySource := ""
	if rc.Worktree != nil && rc.Worktree.ID != "" {
		copySource = rc.Worktree.Path
	}
	req := workspacecreate.Request{
		Project:       project,
		Repos:         repos,
		Name:          p.Name,
		Branch:        p.Branch,
		BaseBranch:    p.BaseBranch,
		Force:         p.Force,
		CopyPaths:     copyPaths,
		CopySource:    copySource,
		ChatID:        chatID,
		OwnerDaemonID: machine.daemonID,
	}
	// The name is the handle agents pass to spawn(worktree=<name>): one live
	// worktree per name. Insert applies the rule, shared with the UI's create.
	wt, err := workspacecreate.Insert(ctx, w.repo, req)
	if err != nil {
		var held *workspacecreate.NameTakenError
		var branchHeld *workspacecreate.BranchHeldError
		switch {
		case errors.As(err, &held):
			return fail("%s", nameTakenReason(held.Holder, p.Name))
		case errors.Is(err, core.ErrWorktreeNameTaken):
			return fail("%s", nameTakenReason(nil, p.Name))
		case errors.As(err, &branchHeld):
			return fail("%v (set the branch parameter)", branchHeld)
		}
		return fail("record the worktree: %v", err)
	}

	// Creation spans many seconds across repos and must not die with the
	// tool call's own deadline half way: that would leave checkouts the
	// rollback never saw.
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeCreateMax)
	defer cancel()
	finishErr := workspacecreate.Finish(createCtx, w.repo, machine, req, wt)
	// Settled either way (ACTIVE or FAILED): tell connected clients.
	w.emitChanged(ctx, machine.userID, project.ID, wt.ID)
	if finishErr != nil {
		return fail("%v (the attempt is recorded as a failed worktree named '%s'; creating again with that name replaces it)", finishErr, p.Name)
	}

	info := w.info(wt, repos)
	info.SessionID = rc.ChatID
	celData := map[string]interface{}{
		"id":          info.ID,
		"name":        info.Name,
		"path":        info.Path,
		"branch":      info.Branch,
		"base_branch": info.BaseBranch,
		"project_id":  info.ProjectID,
		"session_id":  info.SessionID,
		"status":      string(info.Status),
		"checkouts":   info.Checkouts,
	}
	metadata := WorktreeResponseMetadata{
		Action:      "create",
		WorktreeID:  info.ID,
		Path:        info.Path,
		Branch:      info.Branch,
		StoredInCEL: celData,
	}

	var content strings.Builder
	fmt.Fprintf(&content, "Created worktree '%s':\n- ID: %s\n- Path (workspace root): %s\n- Branch: %s\n- Base Branch: %s\n",
		info.Name, info.ID, info.Path, info.Branch, info.BaseBranch)
	writeCheckouts(&content, repos, info.Checkouts)
	fmt.Fprintf(&content, "\nIt appears in the Reliant sidebar. To work in it, pass spawn(worktree=%q) to run a sub-agent there, or use the paths above.\n", info.Name)
	content.WriteString("Worktree data stored in CEL context as 'worktree_data'.")

	return WithResponseMetadata(NewTextResponse(content.String()), metadata), nil
}

func (w *worktreeTool) handleList(ctx context.Context, rc *rctx.ToolContext) (ToolResponse, error) {
	if w.repo == nil {
		return NewTextErrorResponse("Database not available for listing worktrees"), nil
	}
	project, err := w.project(rc)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list worktrees: %v", err)), nil
	}
	rows, err := w.listAll(ctx, project.ID, false)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list worktrees: %v", err)), nil
	}
	if len(rows) == 0 {
		return NewTextResponse("No worktrees found"), nil
	}
	repos := w.reposForDisplay(ctx, rc, project)

	worktrees := make([]*WorktreeInfo, len(rows))
	var content strings.Builder
	fmt.Fprintf(&content, "Found %d worktrees:\n\n", len(rows))
	for i, row := range rows {
		info := w.info(row, repos)
		worktrees[i] = info
		fmt.Fprintf(&content, "%d. %s\n   Status: %s\n", i+1, info.Name, info.Status)
		if info.Path != "" {
			fmt.Fprintf(&content, "   Path: %s\n", info.Path)
		}
		fmt.Fprintf(&content, "   Branch: %s\n", info.Branch)
		if info.BaseBranch != "" {
			fmt.Fprintf(&content, "   Base Branch: %s\n", info.BaseBranch)
		}
		if row.IsMain {
			content.WriteString("   Main checkout: true\n")
		}
		if info.DaemonID != "" {
			fmt.Fprintf(&content, "   Owner machine (daemon): %s\n", info.DaemonID)
		}
		if info.SessionID != "" {
			fmt.Fprintf(&content, "   Chat: %s\n", info.SessionID)
		}
		content.WriteString("\n")
	}
	return WithResponseMetadata(NewTextResponse(content.String()), WorktreeResponseMetadata{Action: "list", Worktrees: worktrees}), nil
}

func (w *worktreeTool) handleGet(ctx context.Context, p *WorktreeParams, rc *rctx.ToolContext) (ToolResponse, error) {
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for get action"), nil
	}
	if w.repo == nil {
		return NewTextErrorResponse("Database not available for getting worktree"), nil
	}
	project, err := w.project(rc)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get worktree: %v", err)), nil
	}
	row, err := w.byName(ctx, project.ID, p.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get worktree: %v", err)), nil
	}
	if row == nil {
		return NewTextErrorResponse(fmt.Sprintf("Worktree '%s' not found", p.Name)), nil
	}
	repos := w.reposForDisplay(ctx, rc, project)
	info := w.info(row, repos)

	celData := map[string]interface{}{
		"id":          info.ID,
		"name":        info.Name,
		"path":        info.Path,
		"branch":      info.Branch,
		"base_branch": info.BaseBranch,
		"project_id":  info.ProjectID,
		"session_id":  info.SessionID,
		"status":      string(info.Status),
		"checkouts":   info.Checkouts,
	}
	metadata := WorktreeResponseMetadata{
		Action:      "get",
		WorktreeID:  info.ID,
		Path:        info.Path,
		Branch:      info.Branch,
		StoredInCEL: celData,
	}

	var content strings.Builder
	fmt.Fprintf(&content, "Worktree: %s\n- ID: %s\n- Status: %s\n- Path (workspace root): %s\n- Branch: %s\n- Base Branch: %s\n- Owner machine (daemon): %s\n- Created: %s\n- Last Active: %s\n",
		info.Name, info.ID, info.Status, info.Path, info.Branch, info.BaseBranch, info.DaemonID,
		info.CreatedAt.Format("2006-01-02 15:04:05"), info.LastActive.Format("2006-01-02 15:04:05"))
	writeCheckouts(&content, repos, info.Checkouts)
	content.WriteString("\nWorktree data stored in CEL context as 'worktree_data'")
	return WithResponseMetadata(NewTextResponse(content.String()), metadata), nil
}

// handleDelete archives the worktree. It never removes the directory itself:
// removing a workspace is the reclaim loop's job (worktreesweep), which does it
// only for a directory that is clean, pushed and not in use, and honours the
// user's "keep everything" setting. The sweep runs in the API server every
// ten minutes over every archived row, with no gRPC call involved.
func (w *worktreeTool) handleDelete(ctx context.Context, p *WorktreeParams, rc *rctx.ToolContext) (ToolResponse, error) {
	fail := func(format string, args ...any) (ToolResponse, error) {
		return NewTextErrorResponse("Failed to delete worktree: " + fmt.Sprintf(format, args...)), nil
	}
	if p.Name == "" {
		return NewTextErrorResponse("Name is required for delete action"), nil
	}
	if err := validateWorktreeName(p.Name); err != nil {
		return fail("%v", err)
	}
	if w.repo == nil {
		return fail("database not available")
	}
	project, err := w.project(rc)
	if err != nil {
		return fail("%v", err)
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return fail("no user in the tool context")
	}
	row, err := w.byName(ctx, project.ID, p.Name)
	if err != nil {
		return fail("%v", err)
	}
	if row == nil {
		return fail("worktree '%s' not found", p.Name)
	}
	switch {
	case row.IsMain:
		return fail("'%s' is the project's main checkout", p.Name)
	case rc.Worktree != nil && rc.Worktree.ID != "" && rc.Worktree.ID == row.ID:
		return fail("'%s' is the worktree this chat is working in; delete it from another chat or the Reliant UI", p.Name)
	case row.Status == int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING):
		return fail("'%s' is still being created", p.Name)
	}
	if busy, err := w.liveChatIn(ctx, userID, project.ID, row.ID); err != nil {
		return fail("%v", err)
	} else if busy != "" {
		return fail("chat %s is working in '%s'; archive that chat or the worktree from the Reliant UI", busy, p.Name)
	}

	if err := w.repo.ArchiveWorktree(ctx, row.ID); err != nil {
		return fail("archive: %v", err)
	}
	w.emitChanged(ctx, userID, project.ID, row.ID)

	content := fmt.Sprintf("Archived worktree '%s'. Its directory is removed later, only if it is clean, pushed and unused; otherwise it is kept and listed in the Reliant inbox.", p.Name)
	return WithResponseMetadata(NewTextResponse(content), WorktreeResponseMetadata{Action: "delete", WorktreeID: row.ID}), nil
}

// workspaceMachine is a workspacecreate.Machine bound to the run's machine.
type workspaceMachine struct {
	RunMachine
	userID   string
	daemonID string
}

// Send implements workspacecreate.Machine.
func (m *workspaceMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s: marshal request: %w", commandType, err)
	}
	raw, err := m.RunMachine.Send(ctx, m.userID, m.daemonID, commandType, body, timeoutMs)
	if err != nil {
		return fmt.Errorf("%s: %w", commandType, err)
	}
	if resp == nil {
		return nil
	}
	if err := json.Unmarshal(raw, resp); err != nil {
		return fmt.Errorf("%s: decode reply: %w", commandType, err)
	}
	return nil
}

// SendDaemonCommand implements repo.DaemonCommander: adoption asks the same
// machine the worktree will be created on.
func (m *workspaceMachine) SendDaemonCommand(ctx context.Context, _, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return m.RunMachine.Send(ctx, m.userID, m.daemonID, commandType, payload, timeoutMs)
}

// runMachine resolves the machine this run's tools execute on.
func (w *worktreeTool) runMachine(ctx context.Context) (*workspaceMachine, error) {
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
	return &workspaceMachine{RunMachine: w.machine, userID: userID, daemonID: daemonID}, nil
}

// project is the chat's project. Its Path is the main checkout, never the
// chat's working directory, which may be inside another workspace.
func (w *worktreeTool) project(rc *rctx.ToolContext) (*db.Project, error) {
	if w.repo == nil {
		return nil, errors.New("database not available")
	}
	if rc == nil || rc.Project == nil {
		return nil, errors.New("this chat has no project")
	}
	return rc.Project, nil
}

// projectRepos lists the repositories a workspace gets a checkout of: the
// chat's, else the registry's, else whatever exists on the machine (the
// registry trails the filesystem after a manual `git init`).
func (w *worktreeTool) projectRepos(ctx context.Context, rc *rctx.ToolContext, project *db.Project, m *workspaceMachine) ([]*core.Repo, error) {
	repos := rc.Repos
	if len(repos) == 0 {
		var err error
		repos, err = w.repo.ListReposByProject(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("list the project's repositories: %w", err)
		}
	}
	if len(repos) == 0 {
		repos = repopkg.AdoptFromDaemon(ctx, w.repo, m, project)
	}
	if len(repos) == 0 {
		return nil, errors.New("the project has no git repositories; initialize one before creating worktrees")
	}
	return repos, nil
}

// reposForDisplay is projectRepos without side effects, for list/get.
func (w *worktreeTool) reposForDisplay(ctx context.Context, rc *rctx.ToolContext, project *db.Project) []*core.Repo {
	if len(rc.Repos) > 0 {
		return rc.Repos
	}
	repos, err := w.repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		logging.Warn("worktree tool: could not list repos", "projectID", project.ID, "error", err)
		return nil
	}
	return repos
}

// listAll pages through the project's worktree rows.
func (w *worktreeTool) listAll(ctx context.Context, projectID string, includeArchived bool) ([]*db.Worktree, error) {
	var out []*db.Worktree
	for offset := 0; ; offset += worktreeListPage {
		rows, err := w.repo.ListWorktrees(ctx, db.WorktreeFilters{
			ProjectID: &projectID, IncludeArchived: includeArchived, Limit: worktreeListPage, Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("look up worktrees: %w", err)
		}
		out = append(out, rows...)
		if len(rows) < worktreeListPage {
			return out, nil
		}
	}
}

// byName returns the project's live worktree called name, or nil. Archived
// rows are not found by name: a name can be reused once its worktree is
// archived, so it identifies the live row only.
func (w *worktreeTool) byName(ctx context.Context, projectID, name string) (*db.Worktree, error) {
	return w.repo.GetLiveWorktreeByName(ctx, projectID, name)
}

// liveChatIn returns the id of a non-archived chat of userID bound to the
// worktree, or "".
func (w *worktreeTool) liveChatIn(ctx context.Context, userID, projectID, worktreeID string) (string, error) {
	chats, err := w.repo.ListChats(ctx, db.ChatFilters{UserID: userID, ProjectID: &projectID, ExcludeArchived: true, Limit: 1000})
	if err != nil {
		return "", fmt.Errorf("look up chats using the worktree: %w", err)
	}
	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktreeID {
			return chat.ID, nil
		}
	}
	return "", nil
}

// emitChanged tells the user's connected clients the worktree list changed.
func (w *worktreeTool) emitChanged(ctx context.Context, userID, projectID, worktreeID string) {
	if err := w.repo.EmitUserRefetch(ctx, userID, db.RefetchWorktreeChanges, db.RefetchOpts{
		ProjectID: &projectID, WorktreeID: &worktreeID,
	}); err != nil {
		logging.Warn("worktree tool: could not emit worktree refetch", "worktreeID", worktreeID, "error", err)
	}
}

// info converts a row, with each repository's checkout under the root.
func (w *worktreeTool) info(row *db.Worktree, repos []*core.Repo) *WorktreeInfo {
	info := &WorktreeInfo{
		ID:         row.ID,
		Name:       row.Name,
		Path:       row.Path,
		Branch:     row.Branch,
		BaseBranch: row.BaseBranch,
		ProjectID:  row.ProjectID,
		Status:     worktreeStatusName(row.Status),
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		LastActive: row.LastActive,
	}
	if row.ChatID != nil {
		info.SessionID = *row.ChatID
	}
	if row.DaemonID != nil {
		info.DaemonID = *row.DaemonID
	}
	if row.Path != "" && len(repos) > 0 {
		info.Checkouts = make(map[string]string, len(repos))
		for _, repo := range repos {
			info.Checkouts[repo.Name] = checkoutPath(row.Path, repo)
		}
	}
	return info
}

// checkoutPath is where a repository's checkout sits in a workspace. The
// workspace belongs to the user's machine, so the path rules are its own.
func checkoutPath(root string, repo *core.Repo) string {
	if repo.RelativePath == "" {
		return root
	}
	return ospath.Join(root, repo.RelativePath)
}

func writeCheckouts(b *strings.Builder, repos []*core.Repo, checkouts map[string]string) {
	if len(checkouts) == 0 {
		return
	}
	b.WriteString("- Checkouts (one per repository, each on the branch above):\n")
	for _, repo := range repos {
		if path, ok := checkouts[repo.Name]; ok {
			fmt.Fprintf(b, "  - %s: %s\n", repo.Name, path)
		}
	}
}

func worktreeStatusName(status int32) WorktreeStatus {
	switch reliantv1.WorktreeStatus(status) {
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_COMPLETED:
		return WorktreeStatusCompleted
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_ABANDONED:
		return WorktreeStatusAbandoned
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_MERGING:
		return WorktreeStatusMerging
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING:
		return WorktreeStatusCreating
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED:
		return WorktreeStatusFailed
	default:
		return WorktreeStatusActive
	}
}

// nameTakenReason says why a name cannot be used. Only a live worktree holds a
// name (a failed one is replaced by a retry, an archived one releases it), so
// holder, nil when a concurrent create won the race, is always live.
func nameTakenReason(holder *db.Worktree, name string) string {
	if holder != nil && holder.Status == int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING) {
		return fmt.Sprintf("a worktree named '%s' is still being created; pick another name or wait for it", name)
	}
	return fmt.Sprintf("a worktree named '%s' already exists in this project; pick another name, or delete the old one (action delete) to reuse the name", name)
}

// validateWorktreeName refuses a name that is not one plain path segment: it
// becomes the prefix of a directory on the user's machine.
func validateWorktreeName(name string) error {
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid worktree name %q: use a single directory name, without slashes", name)
	}
	return nil
}

// IsReadOnly implements ReadOnlyTool
func (w *worktreeTool) IsReadOnly() bool {
	return false // Worktree operations modify file system
}
