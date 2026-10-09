// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

const worktreeDaemonCommandTimeoutMs int32 = 120_000

// worktreeReadCommandTimeoutMs bounds the interactive read-path commands the
// UI issues repeatedly (git_changes / git_status / git_commits). At the 120s
// mutation budget, one slow daemon answer pinned a browser connection per
// stacked poll for two minutes — enough to exhaust Chromium's 6-connection
// HTTP/1.1 pool in dev (renderer → Vite is plain http) and starve every other
// RPC behind it. Reads that exceed this budget should fail fast and let the
// next poll retry.
const worktreeReadCommandTimeoutMs int32 = 30_000

// WorktreeService implements the WorktreeService RPC handlers
type WorktreeService struct {
	reliantv1connect.UnimplementedWorktreeServiceHandler
	database     db.Repository
	tempClient   client.Client
	daemonRouter toolexec.DaemonRouter
	wake         machineWake
	// settler removes an archived worktree's directory when it is provably
	// safe. Nil in tests that do not exercise it.
	settler worktreeSettler
}

// worktreeSettler is what archiving needs from the sweep: one immediate
// attempt to settle the directory of a worktree that was just archived.
type worktreeSettler interface {
	Settle(ctx context.Context, userID string, cand *core.ReclaimCandidate)
	SettleBlocking(ctx context.Context, userID string, cand *core.ReclaimCandidate) *core.CleanupMetadata
}

// NewWorktreeService creates a new WorktreeService
func NewWorktreeService(database db.Repository, tempClient client.Client, daemonRouter toolexec.DaemonRouter) *WorktreeService {
	return &WorktreeService{
		database:     database,
		tempClient:   tempClient,
		daemonRouter: daemonRouter,
		wake:         machineWake{router: daemonRouter, owners: database},
	}
}

// worktreeOwner is the daemon holding a worktree's checkout on disk, or "" when
// the row names none — the main checkout, which every machine with the project
// has, and rows from before ownership was recorded. "" leaves default
// resolution.
//
// Every command about an existing worktree goes to its owner. The checkout
// exists nowhere else, so sending one to the default daemon reports a missing
// directory at best, and at worst acts on a same-named path on another machine.
func worktreeOwner(worktree *db.Worktree) string {
	if worktree == nil || worktree.DaemonID == nil {
		return ""
	}
	return *worktree.DaemonID
}

// relockActive tells the owning daemon to lock this worktree's checkouts as
// active and to RETIRE the archive's fence, so nothing built from a read taken
// before the restore can claim or remove the directory afterwards.
//
// It is synchronous and its answer is checked:
//   - "gone": a removal already finished. The row is recorded as removed and the
//     unarchive is refused (restore it with recreate); flipping it to active over
//     a missing directory would leave a broken workspace.
//   - anything but "locked" (unreachable, held such as a parked checkout, error):
//     the row stays archived and an error is returned, never an active row with
//     no verified lock.
//
// It returns true only when the machine answered "locked".
func (s *WorktreeService) relockActive(ctx context.Context, userID string, worktree *db.Worktree) (bool, error) {
	if worktree.Path == "" || worktree.DaemonID == nil || *worktree.DaemonID == "" {
		return true, nil
	}
	fence := ""
	if worktree.DeletedAt != nil {
		fence = strconv.FormatInt(worktree.DeletedAt.UnixNano(), 10)
	}
	req := struct {
		Worktrees []map[string]any `json:"worktrees"`
	}{Worktrees: []map[string]any{{"id": worktree.ID, "path": worktree.Path, "state": "active", "retire": fence}}}
	var resp struct {
		Results []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
			Detail  string `json:"detail"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, *worktree.DaemonID, "worktree.reconcile", req, &resp); err != nil {
		// Do not flip the row to active over a directory we could not re-lock:
		// it stays archived and the user retries when the machine is reachable.
		logging.Warn("Could not reach the machine to re-lock an archived workspace; leaving it archived", "worktreeID", worktree.ID, "error", err)
		return false, fmt.Errorf("the machine that holds this workspace is unreachable, so it was left archived: %w", err)
	}
	if len(resp.Results) == 0 {
		return false, fmt.Errorf("the machine did not answer for this workspace")
	}
	res := resp.Results[0]
	switch res.Outcome {
	case "locked":
		return true, nil
	case "gone":
		_ = s.database.MergeWorktreeCleanupMetadata(ctx, worktree.ID, func(m *db.CleanupMetadata, _ bool) bool {
			m.DirectoryDeleted = true
			return true
		})
		return false, nil
	case "held":
		return false, fmt.Errorf("could not restore: %s (%s)", res.Detail, res.Reason)
	case "error":
		return false, fmt.Errorf("the machine could not re-lock this workspace: %s", res.Error)
	}
	return false, fmt.Errorf("could not restore: the machine answered %q", res.Outcome)
}

// WithSettler enables settling an archived worktree's directory immediately.
func (s *WorktreeService) WithSettler(settler worktreeSettler) *WorktreeService {
	s.settler = settler
	return s
}

// settleArchived asks the owning daemon to remove an archived worktree's
// directory if that is safe, on a context detached from the request: archiving
// has already succeeded, and a client that disconnects must not abandon a
// half-finished removal. The directory is the daemon's to judge; whatever it
// declines is recorded and offered in the storage inbox item.
func (s *WorktreeService) settleArchived(ctx context.Context, userID string, worktree *db.Worktree, projectPath string) {
	if s.settler == nil || worktree.Path == "" {
		return
	}
	archived, err := s.database.GetWorktree(ctx, worktree.ID)
	if err != nil || archived == nil {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	go func() {
		defer cancel()
		s.settler.Settle(bg, userID, &core.ReclaimCandidate{Worktree: archived, OwnerUserID: userID, ProjectPath: projectPath})
	}()
}

// sendWorktreeDaemonCommand sends a command to daemonID ("" = the user's
// default daemon) and unmarshals the response, with the default (mutation)
// timeout budget.
//
// Pass the daemon explicitly. Worktree creation issues one command per nested
// repo, and all of them — plus the id recorded on the row — must name the same
// machine, so it resolves the daemon once and passes it to every send.
func (s *WorktreeService) sendWorktreeDaemonCommand(ctx context.Context, userID, daemonID, commandType string, payload interface{}, resp interface{}) error {
	return s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonID, commandType, payload, resp, worktreeDaemonCommandTimeoutMs)
}

// sendWorktreeDaemonCommandTimeout is sendWorktreeDaemonCommand with an
// explicit timeout — use worktreeReadCommandTimeoutMs for the polled read
// paths so a slow daemon can't pin connections for the full mutation budget.
func (s *WorktreeService) sendWorktreeDaemonCommandTimeout(ctx context.Context, userID, daemonID, commandType string, payload interface{}, resp interface{}, timeoutMs int32) error {
	return s.sendToMachine(ctx, userID, wakeTarget{daemonID: daemonID}, commandType, payload, resp, timeoutMs)
}

// sendToMachine sends a command to the target machine. Every worktree command
// goes through here, so a machine found asleep is woken (machineWake) and the
// request fails as waking, for the client to retry once it is up.
func (s *WorktreeService) sendToMachine(ctx context.Context, userID string, target wakeTarget, commandType string, payload interface{}, resp interface{}, timeoutMs int32) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	var respBytes []byte
	if target.daemonID == "" {
		respBytes, err = s.daemonRouter.SendDaemonCommand(ctx, userID, commandType, payloadBytes, timeoutMs)
	} else {
		respBytes, err = s.daemonRouter.SendDaemonCommandToDaemon(ctx, userID, target.daemonID, commandType, payloadBytes, timeoutMs)
	}
	if err != nil {
		err = s.wake.afterFailure(ctx, userID, target, err)
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
			HeldReason:       w.CleanupMetadata.HeldReason,
			HeldDetail:       w.CleanupMetadata.HeldDetail,
			SizeBytes:        w.CleanupMetadata.SizeBytes,
			SnapshotRefs:     w.CleanupMetadata.SnapshotRefs,
		}
	}

	return proto
}

// listProjectRepos returns the project's nested repos, rejecting projects with
// none. Empty list is a precondition failure rather than success-with-zero so
// callers can rely on at least one fan-out target.
func (s *WorktreeService) listProjectRepos(ctx context.Context, project *db.Project) ([]*core.Repo, error) {
	repos, err := s.database.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}
	if len(repos) == 0 {
		// The registry trails the filesystem when a repo was created outside
		// the tracked flows (e.g. `git init` in the workspace terminal), so
		// ask the daemon what actually exists and adopt it before refusing.
		repos = repopkg.AdoptFromDaemon(ctx, s.database, s.daemonRouter, project)
	}
	if len(repos) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("project has no git repos; initialize one or add a nested repo before creating worktrees"))
	}
	return repos, nil
}

// resolveRepoPath resolves a per-RPC repo_id selector against the worktree's
// project repo set and returns the absolute git checkout path inside the
// workspace. The rules:
//
//   - empty repoID + project has 0 or 1 repos -> use worktree.Path as-is
//     (legacy single-repo behavior; works for both "project root is a repo"
//     and "no repos yet but daemon-side path is still a git checkout").
//   - empty repoID + project has 2+ repos -> InvalidArgument; multi-repo
//     callers must specify which repo they're acting on.
//   - non-empty repoID -> look up the repo, ensure it belongs to this
//     worktree's project, and return <worktree.Path>/<repo.relative_path>.
//   - repoID not in the project's repo set -> NotFound.
//
// Returns the resolved absolute path and the resolved repo (nil when falling
// through to legacy single-repo behavior with no Repo rows).
func (s *WorktreeService) resolveRepoPath(ctx context.Context, worktree *db.Worktree, repoID string) (string, *core.Repo, error) {
	repos, err := s.database.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return "", nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}

	if repoID == "" {
		if len(repos) > 1 {
			return "", nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("repo_id required in multi-repo projects"))
		}
		// 0 repos: fall through to worktree path (e.g. project initialized
		// before nested-repo migration). 1 repo: prefer the registered repo's
		// relative_path so multi-repo workspace layouts still work for the
		// single-repo case (relative_path is "" for a project-root repo, so
		// this reduces to worktree.Path).
		if len(repos) == 1 {
			return filepath.Join(worktree.Path, repos[0].RelativePath), repos[0], nil
		}
		return worktree.Path, nil, nil
	}

	for _, r := range repos {
		if r.ID == repoID {
			return filepath.Join(worktree.Path, r.RelativePath), r, nil
		}
	}
	return "", nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repo not in project"))
}

// resolveRepoBaseBranch picks the right base branch for a per-repo write
// operation (e.g. CreatePR). Lookup order:
//
//  1. worktree.BaseBranches[repo.ID] — the per-repo override captured at
//     create time (set when multi-repo workspaces have heterogeneous
//     defaults like main/master/develop).
//  2. worktree.BaseBranch — the legacy single value, canonical for
//     single-repo and a sane fallback when the per-repo map is empty/missing
//     this entry.
//  3. "" — let the daemon auto-detect via gh / git remote show.
//
// repo may be nil when the project has no Repo rows registered (legacy
// pre-migration shape); we fall through to the legacy single value.
func (s *WorktreeService) resolveRepoBaseBranch(worktree *db.Worktree, repo *core.Repo) string {
	if repo != nil {
		if b, ok := worktree.BaseBranches[repo.ID]; ok && b != "" {
			return b
		}
	}
	return worktree.BaseBranch
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.validate_path", map[string]string{"path": worktree.Path}, &resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot access worktree directory: %w", err))
	}
	if !resp.Exists {
		if resp.Error == "not_found" {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worktree directory does not exist: %s; the worktree may need to be recreated", worktree.Path))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot access worktree directory: %s", resp.Error))
	}

	return nil
}

// wakeIfAsleep asks the target machine a cheap question before a write that
// would otherwise meet an asleep machine too late: a create that persists its
// row and makes the checkouts in the background, or a delete whose cleanup is
// best-effort. An asleep machine is woken and the call fails as waking (see
// machineWake), so nothing is recorded until a retry finds the machine up.
//
// Only a wake is returned. Any other failure of the probe is left for the
// operation itself to meet, as before.
func (s *WorktreeService) wakeIfAsleep(ctx context.Context, userID string, target wakeTarget, path string) error {
	var resp struct {
		Exists bool `json:"exists"`
	}
	err := s.sendToMachine(ctx, userID, target, "worktree.validate_path", map[string]string{"path": path}, &resp, worktreeReadCommandTimeoutMs)
	if isMachineWaking(err) {
		return err
	}
	return nil
}

// =============================================================================
// CRUD Operations
// =============================================================================

// CreateWorktree creates a workspace-level worktree spanning all of the
// project's nested repos. Steps:
//  1. Validate inputs and resolve the project + its nested repos.
//  2. Generate a workspace UUID. Instruct the daemon to create one git
//     worktree per repo, all under the same workspace dir
//     (<HOME>/.reliant/worktrees/<workspace_id>/<repo.relative_path>).
//  3. On full success, persist a single Worktree row whose Path is the
//     workspace root and return it.
//  4. On partial failure, best-effort rollback (delete created git worktrees)
//     and return an error.
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
	// copy_files are exact paths relative to the workspace root. Rejected
	// HERE, synchronously, so a typo or an escaping path is an error the
	// caller sees — not a workspace that silently lacks its .env.
	copyPaths, err := copypath.CleanAll(req.Msg.CopyFiles)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	repos, err := s.listProjectRepos(ctx, project)
	if err != nil {
		return nil, err
	}

	globalBase := ""
	if req.Msg.BaseBranch != nil {
		globalBase = *req.Msg.BaseBranch
	}

	// Resolve a source workspace if specified: copy_files are copied from its
	// root instead of the live project root.
	var sourceWorktree *db.Worktree
	var sourceWorkspace string
	if req.Msg.SourceWorktreeId != nil && *req.Msg.SourceWorktreeId != "" {
		if err := s.worktreeBelongsToUser(ctx, *req.Msg.SourceWorktreeId, userID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source worktree not found"))
		}
		sourceWorktree, err = s.database.GetWorktree(ctx, *req.Msg.SourceWorktreeId)
		if err != nil {
			logging.Warn("Source worktree not found, falling back to project paths",
				"sourceWorktreeId", *req.Msg.SourceWorktreeId, "error", err)
			sourceWorktree = nil
		} else {
			sourceWorkspace = sourceWorktree.Path
		}
	}

	// Resolve the owning daemon ONCE up front: the chat's machine when the
	// worktree is created for a chat (see placeNewWorktree). Every per-repo
	// worktree.create (and the rollback/force-cleanup commands) must target
	// this same daemon so the worktree's N nested checkouts all land on one
	// machine, and that id is recorded on the row for tool execution to route
	// back to (a branch chat's worktree exists on disk only here). Fail fast
	// if no daemon is reachable — creating a worktree row that points at a
	// directory on no daemon is worse than a clear up-front error.
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, req.Msg.ChatId, sourceWorktree, "create")
	if err != nil {
		return nil, err
	}
	ownerDaemonID := placement.daemonID
	// The checkouts are made after this returns, so an asleep machine would
	// leave a row that fails in the background. Wake it now instead: nothing
	// has been recorded yet, so the client's retry creates the worktree, once,
	// on a machine that is up.
	if err := s.wakeIfAsleep(ctx, userID, placement, project.Path); err != nil {
		return nil, err
	}

	// An idempotency key makes a retry safe. Creation returns before the work
	// finishes, so a client that loses the response cannot tell "failed" from
	// "succeeded, reply dropped" — without this the natural retry produces a
	// second workspace with a second on-disk checkout.
	if req.Msg.IdempotencyKey != nil && *req.Msg.IdempotencyKey != "" {
		existing, err := s.database.GetWorktreeByIdempotencyKey(ctx, project.ID, *req.Msg.IdempotencyKey)
		if err != nil {
			logging.Error("Failed to check worktree idempotency key", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create worktree"))
		}
		if existing != nil {
			return connect.NewResponse(&reliantv1.CreateWorktreeResponse{
				Worktree: worktreeToProto(existing),
			}), nil
		}
	}

	// Persist the row BEFORE any daemon work, then return. Creation spans
	// 30-120s across repos, and holding the response open for that long meant
	// a client disconnect (a phone backgrounding, a dropped network) cancelled
	// the request context and aborted the work mid-flight — including the
	// rollback that was supposed to clean up the half-created state, which is
	// how orphaned directories were left on the daemon.
	//
	// Path is empty until the daemon reports where it landed; the row carries
	// CREATING so callers render a pending workspace rather than a broken one.
	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}
	var idempotencyKey *string
	if req.Msg.IdempotencyKey != nil && *req.Msg.IdempotencyKey != "" {
		idempotencyKey = req.Msg.IdempotencyKey
	}
	createReq := workspacecreate.Request{
		Project:        project,
		Repos:          repos,
		Name:           req.Msg.Name,
		Branch:         req.Msg.Branch,
		BaseBranch:     globalBase,
		BaseBranches:   req.Msg.BaseBranches,
		Force:          req.Msg.Force,
		CopyPaths:      copyPaths,
		CopySource:     sourceWorkspace,
		ChatID:         chatID,
		OwnerDaemonID:  ownerDaemonID,
		IdempotencyKey: idempotencyKey,
	}
	worktree, err := workspacecreate.Insert(ctx, s.database, createReq)
	if err != nil {
		return nil, insertWorktreeError(err, req.Msg.Name)
	}

	// Detached from the request context so the work survives the client going
	// away — that is the entire point of the change. Auth values and tracing
	// carry over; only cancellation is severed. The explicit timeout is what
	// keeps this from outliving a stuck daemon forever.
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeCreateBudget)
	// The goroutine mutates its worktree as the create settles, while this
	// function is still reading the same struct to build the response. Hand it
	// a copy so the two never touch the same memory.
	pending := *worktree
	go func() {
		defer cancel()
		s.finishWorktreeCreate(bgCtx, userID, createReq, &pending)
	}()

	return connect.NewResponse(&reliantv1.CreateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// insertWorktreeError maps a workspacecreate.Insert failure to its wire error.
func insertWorktreeError(err error, name string) error {
	var branchHeld *workspacecreate.BranchHeldError
	switch {
	case errors.Is(err, core.ErrWorktreeNameTaken):
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project; archive or rename it, or choose another name", name))
	case errors.As(err, &branchHeld):
		return connect.NewError(connect.CodeFailedPrecondition, branchHeld)
	}
	logging.Error("Failed to create worktree row", "error", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create worktree"))
}

// worktreeCreateBudget bounds the detached creation goroutine. Generous
// because it covers every repo in a multi-repo project sequentially, but
// finite: without it a wedged daemon would leak a goroutine and leave the row
// in CREATING forever.
const worktreeCreateBudget = 10 * time.Minute

// finishWorktreeCreate performs the daemon-side worktree creation and settles
// the row to ACTIVE or FAILED, then tells clients. It runs on a context
// detached from the client request; nothing here may assume a caller is still
// listening.
func (s *WorktreeService) finishWorktreeCreate(ctx context.Context, userID string, req workspacecreate.Request, worktree *db.Worktree) {
	_ = workspacecreate.Finish(ctx, s.database, worktreeMachine{s: s, userID: userID, daemonID: req.OwnerDaemonID}, req, worktree)
	s.emitWorktreeChanged(ctx, userID, req.Project.ID, worktree.ID)
}

// worktreeMachine adapts the service's daemon send (which wakes a sleeping
// machine on failure) to workspacecreate.Machine, bound to one owner daemon.
type worktreeMachine struct {
	s        *WorktreeService
	userID   string
	daemonID string
}

func (m worktreeMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	return m.s.sendWorktreeDaemonCommandTimeout(ctx, m.userID, m.daemonID, commandType, payload, resp, timeoutMs)
}

// emitWorktreeChanged tells connected clients a worktree's state settled.
//
// This rides `user_updates`, which is a sequenced Postgres table replayed on
// reconnect via `sinceSeq` — not a fire-and-forget signal. That is what makes
// asynchronous creation safe on a phone: an app backgrounded through the whole
// 30-120s create reconnects and receives the completion it slept through.
func (s *WorktreeService) emitWorktreeChanged(ctx context.Context, userID, projectID, worktreeID string) {
	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchWorktreeChanges, db.RefetchOpts{
		ProjectID:  &projectID,
		WorktreeID: &worktreeID,
	}); err != nil {
		logging.Error("Failed to emit worktree refetch", "error", err, "worktreeID", worktreeID)
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

	// The directory is no longer the client's call: the owning daemon removes it
	// when it is provably safe and otherwise holds it for the user to clean up
	// knowingly. A permanent delete and a branch delete still reach the machine
	// from here, so wake an asleep one first; the client retries.
	if (req.Msg.DeleteGitBranch || isPermanentDelete) && project != nil {
		if err := s.wakeIfAsleep(ctx, userID, wakeTarget{daemonID: worktreeOwner(worktree)}, project.Path); err != nil {
			return nil, err
		}
	}

	// The directory is no longer the client's call. The owning daemon removes
	// it when it is provably safe (clean, pushed or merged, not in use) and
	// otherwise holds it for the user to clean up knowingly; see settleArchived.
	if isPermanentDelete {
		// The row is the only thing that lets reliant find this directory
		// again, so it may only go once the machine has removed the directory
		// (or it never existed). Otherwise the directory stays locked with
		// reliant's reason forever and nothing will ever clean it up.
		if worktree.Path != "" && project != nil && s.settler != nil {
			meta := s.settler.SettleBlocking(ctx, userID, &core.ReclaimCandidate{Worktree: worktree, OwnerUserID: userID, ProjectPath: project.Path})
			if meta == nil || !meta.DirectoryDeleted {
				reason := "the machine has not removed its directory yet"
				if meta != nil && meta.HeldReason != "" {
					reason = "its directory is still on disk (" + meta.HeldReason + "): clean it up from your Inbox first"
				}
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete this workspace permanently: %s", reason))
			}
		}
		deletedBranch := false
		if req.Msg.DeleteGitBranch && project != nil && worktree.Branch != "" {
			deletedBranch = s.cleanupWorktreeBranch(ctx, userID, worktreeOwner(worktree), project.Path, worktree.Branch)
		}
		if err := s.database.DeleteWorktree(ctx, req.Msg.WorktreeId); err != nil {
			logging.Error("Failed to permanently delete worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to permanently delete worktree"))
		}

		return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
			Message:           "Worktree permanently deleted",
			DeletedBranch:     deletedBranch,
			IsPermanentDelete: true,
		}), nil
	}

	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, req.Msg.DeleteGitBranch)

	// Archive the worktree
	if err := s.database.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to archive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)
	if project != nil {
		s.settleArchived(ctx, userID, worktree, project.Path)
	}

	return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
		Message:           "Worktree and associated chats archived successfully",
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

	// The branch is checked out in the directory until the machine removes it,
	// and git refuses to delete a checked-out branch, so deletion is requested
	// here and carried out after the machine reports the directory removed.
	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, req.Msg.DeleteGitBranch)

	// Archive the worktree
	if err := s.database.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to archive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)
	if project != nil {
		s.settleArchived(ctx, userID, worktree, project.Path)
	}

	return connect.NewResponse(&reliantv1.ArchiveWorktreeResponse{
		Message: "Worktree and associated chats archived successfully",
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

	// A directory that is already gone cannot be unarchived into a working
	// workspace: the user restores it with RecreateWorktree, which rebuilds it.
	if worktree.CleanupMetadata != nil && worktree.CleanupMetadata.DirectoryDeleted {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this workspace's files were removed; restore it with recreate"))
	}

	// Refuse a name collision before the machine is touched: re-locking the
	// checkouts for a row that then cannot be restored would leave them locked
	// as live under an archived row.
	if holder, err := s.database.GetLiveWorktreeByName(ctx, worktree.ProjectID, worktree.Name); err == nil && holder != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
	}

	// Take the archive fence off the directory FIRST, and retire it: the machine
	// re-locks the checkouts as "restored <fence>", so a removal already in
	// flight for this archive finds a lock it does not own and refuses, and no
	// claim built before now can use that fence again. See relockActive.
	proceed, err := s.relockActive(ctx, userID, worktree)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !proceed {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this workspace's files were removed; restore it with recreate"))
	}

	if err := s.database.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		if errors.Is(err, core.ErrWorktreeNameTaken) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
		}
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

// cleanupWorktreeBranch deletes the worktree's branch from the project clone on
// daemonID — the owner's clone, which is where creating the worktree made it.
func (s *WorktreeService) cleanupWorktreeBranch(ctx context.Context, userID, daemonID, projectPath, branch string) bool {
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.delete_branch", map[string]string{
		"project_path": projectPath,
		"branch":       branch,
	}, &resp); err != nil {
		logging.Warn("Failed to delete git branch via daemon", "error", err, "branch", branch)
		return false
	}
	return resp.Deleted
}

// storeCleanupMetadata records that the user wants the branch deleted once the
// directory is gone. Whether the directory went is the daemon's to report, so it
// is never set here, and what is already stored (held reason, snapshot refs) is
// kept.
func (s *WorktreeService) storeCleanupMetadata(ctx context.Context, worktreeID string, deleteBranch bool) {
	if !deleteBranch {
		return
	}
	err := s.database.MergeWorktreeCleanupMetadata(ctx, worktreeID, func(m *db.CleanupMetadata, _ bool) bool {
		m.DeleteBranch = true
		return true
	})
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
			if workflowID := chat.MainThreadID(); workflowID != "" {
				_ = s.tempClient.CancelWorkflow(ctx, workflowID, "")
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

// ImportWorktree adopts a git worktree made outside Reliant. The path must be
// a linked worktree the daemon currently reports for repo_id that no row
// tracks and that is not Reliant's own; the client's word is never enough.
//
//   - Single-repo project: the row is registered in place (Path = the checkout).
//     Nothing moves and nothing is created. It is NOT locked: the reclaimer
//     only settles rows under the daemon's worktrees root (anything else is
//     "foreign" and left alone), so a lock would protect nothing.
//   - Multi-repo project: the checkout is moved to <workspace>/<repo rel> with
//     `git worktree move`, then the other repos get checkouts on the same
//     branch, exactly like a UI-made workspace. It needs confirm_move. If any
//     step after the move fails the checkout is moved back (see
//     workspacecreate.ExistingCheckout) and the row ends FAILED.
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
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}
	repos, err := s.listProjectRepos(ctx, project)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*core.Repo, len(repos))
	for _, r := range repos {
		byID[r.ID] = r
	}
	var repo *core.Repo
	switch {
	case req.Msg.RepoId != "":
		if repo = byID[req.Msg.RepoId]; repo == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repo not in project"))
		}
	case len(repos) == 1:
		repo = repos[0]
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_id is required in multi-repo projects"))
	}
	moves := importMoves(byID)
	if moves && !req.Msg.ConfirmMove {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("importing this worktree moves its directory into a new workspace; confirm the move (confirm_move) to continue"))
	}

	placement, err := s.placeNewWorktree(ctx, userID, project.ID, req.Msg.ChatId, nil, "import")
	if err != nil {
		return nil, err
	}
	if err := s.wakeIfAsleep(ctx, userID, placement, project.Path); err != nil {
		return nil, err
	}
	ownerDaemonID := placement.daemonID

	// Re-discover on the daemon and take the entry from there.
	exclude, err := s.trackedWorktreePaths(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	repoPath := filepath.Join(project.Path, repo.RelativePath)
	var found struct {
		Worktrees []discoveredEntry `json:"worktrees"`
	}
	payload := map[string]any{"repos": []discoverRepoRef{{RepoID: repo.ID, Path: repoPath}}, "exclude_roots": exclude}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.discover_repos", payload, &found, worktreeReadCommandTimeoutMs); err != nil {
		if isMachineWaking(err) {
			return nil, err
		}
		logging.Error("Failed to validate import path via daemon", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to validate path"))
	}
	want := filepath.Clean(req.Msg.Path)
	var entry *discoveredEntry
	for i := range found.Worktrees {
		if !found.Worktrees[i].Prunable && filepath.Clean(found.Worktrees[i].Path) == want {
			entry = &found.Worktrees[i]
			break
		}
	}
	if entry == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("%s is not an untracked linked worktree of repo '%s'; refresh the list and try again", req.Msg.Path, repo.Name))
	}
	if entry.Branch == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s has a detached HEAD, so there is no branch to adopt; create one in it first (git switch -c <branch>) and try again", entry.Path))
	}

	name := entry.Name
	if req.Msg.Name != nil && *req.Msg.Name != "" {
		name = *req.Msg.Name
	}
	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}
	createReq := workspacecreate.Request{
		Project:       project,
		Repos:         repos,
		Name:          name,
		Branch:        entry.Branch,
		ChatID:        chatID,
		OwnerDaemonID: ownerDaemonID,
	}
	if moves {
		createReq.WorkspaceID = worktreepath.WorkspaceDirName(project.Name, name)
	}
	worktree, err := workspacecreate.Insert(ctx, s.database, createReq)
	if err != nil {
		return nil, insertWorktreeError(err, name)
	}
	machine := worktreeMachine{s: s, userID: userID, daemonID: ownerDaemonID}

	if !moves {
		var def struct {
			DefaultBranch string `json:"default_branch"`
		}
		_ = machine.Send(ctx, "worktree.get_default_branch", map[string]string{"repo_path": repoPath}, &def, worktreeReadCommandTimeoutMs)
		createReq.Existing = map[string]workspacecreate.ExistingCheckout{
			repo.ID: {Path: entry.Path, Base: def.DefaultBranch},
		}
		if err := workspacecreate.Finish(ctx, s.database, machine, createReq, worktree); err != nil {
			s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to import worktree"))
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
		return connect.NewResponse(&reliantv1.ImportWorktreeResponse{Worktree: worktreeToProto(worktree)}), nil
	}

	var moved struct {
		Success      bool   `json:"success"`
		WorktreePath string `json:"worktree_path"`
		BaseBranch   string `json:"base_branch"`
		Error        string `json:"error"`
	}
	moveErr := machine.Send(ctx, "worktree.adopt_move", map[string]string{
		"repo_path":    repoPath,
		"src":          entry.Path,
		"workspace_id": createReq.WorkspaceID,
		"sub_path":     repo.RelativePath,
		"worktree_id":  worktree.ID,
	}, &moved, workspaceMoveTimeoutMs)
	if moveErr != nil || !moved.Success {
		// Nothing has moved (git refused, or the command never ran): the row
		// is FAILED with no path and the user's checkout is where it was.
		worktree.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
		worktree.Path = ""
		worktree.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateWorktree(ctx, worktree); err != nil {
			logging.Error("Failed to mark adopted worktree failed", "error", err, "worktreeID", worktree.ID)
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
		if moveErr != nil {
			if isMachineWaking(moveErr) {
				return nil, moveErr
			}
			logging.Error("adopt_move failed", "error", moveErr)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to move the worktree"))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("could not move %s: %s", entry.Path, moved.Error))
	}

	createReq.Existing = map[string]workspacecreate.ExistingCheckout{
		repo.ID: {Path: moved.WorktreePath, Base: moved.BaseBranch, OriginalPath: entry.Path},
	}
	// The other repos' checkouts take as long as a create, so finish in the
	// background like CreateWorktree; the row stays CREATING until then.
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeCreateBudget)
	pending := *worktree
	go func() {
		defer cancel()
		s.finishWorktreeCreate(bgCtx, userID, createReq, &pending)
	}()
	return connect.NewResponse(&reliantv1.ImportWorktreeResponse{Worktree: worktreeToProto(worktree)}), nil
}

// workspaceMoveTimeoutMs bounds adopt_move: a rename on one filesystem, but
// git also rewrites the worktree's records.
const workspaceMoveTimeoutMs int32 = 60_000

// discoverRepoRef is a project repo as the daemon's discover/prune commands
// take it.
type discoverRepoRef struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

// importMoves reports whether importing a worktree of this project moves the
// directory (a multi-repo project, or a repo below the project root) rather
// than registering it in place.
func importMoves(repos map[string]*core.Repo) bool {
	if len(repos) > 1 {
		return true
	}
	for _, r := range repos {
		if rel := filepath.Clean(r.RelativePath); rel != "." && rel != "" {
			return true
		}
	}
	return false
}

// trackedWorktreePaths lists every path a row of the project holds, archived
// rows included: an archived row's directory may still exist, and its
// checkout is not "outside Reliant".
func (s *WorktreeService) trackedWorktreePaths(ctx context.Context, projectID string) ([]string, error) {
	tracked, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &projectID, IncludeArchived: true, Limit: 100000,
	})
	if err != nil {
		logging.Error("Failed to list worktrees", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list worktrees"))
	}
	paths := make([]string, 0, len(tracked))
	for _, wt := range tracked {
		if wt.Path != "" {
			paths = append(paths, wt.Path)
		}
	}
	return paths, nil
}

type discoveredEntry struct {
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	Locked         bool   `json:"locked"`
	Prunable       bool   `json:"prunable"`
	PrunableReason string `json:"prunable_reason"`
}

// projectRepoRefs lists the project's repos with their checkout paths on the
// daemon. A repo with relative path "" or "." is the project root itself.
func (s *WorktreeService) projectRepoRefs(ctx context.Context, project *db.Project) ([]discoverRepoRef, map[string]*core.Repo, error) {
	repos, err := s.database.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}
	refs := make([]discoverRepoRef, 0, len(repos))
	byID := make(map[string]*core.Repo, len(repos))
	for _, r := range repos {
		refs = append(refs, discoverRepoRef{RepoID: r.ID, Path: filepath.Join(project.Path, r.RelativePath)})
		byID[r.ID] = r
	}
	return refs, byID, nil
}

func staleToProto(e discoveredEntry, byID map[string]*core.Repo) *reliantv1.StaleWorktree {
	st := &reliantv1.StaleWorktree{Path: e.Path, RepoId: e.RepoID, Reason: e.PrunableReason}
	if r := byID[e.RepoID]; r != nil {
		st.RepoName = r.Name
	}
	return st
}

// DiscoverWorktrees lists the git worktrees of each of the project's repos
// that Reliant does not track, and git's stale records separately. It is
// read-only.
func (s *WorktreeService) DiscoverWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.DiscoverWorktreesRequest],
) (*connect.Response[reliantv1.DiscoverWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", req.Msg.ProjectId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}
	refs, byID, err := s.projectRepoRefs(ctx, project)
	if err != nil {
		return nil, err
	}

	exclude, err := s.trackedWorktreePaths(ctx, project.ID)
	if err != nil {
		return nil, err
	}

	// A background poll (the sidebar hint) never wakes a suspended machine:
	// it answers Unavailable and the hint stays hidden.
	if req.Msg.Background {
		ctx = withoutWake(ctx)
	}

	// The same machine a new workspace for this project would be placed on.
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, "discover")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Worktrees     []discoveredEntry `json:"worktrees"`
		WorktreesRoot string            `json:"worktrees_root"`
	}
	payload := map[string]any{"repos": refs, "exclude_roots": exclude}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.discover_repos", payload, &resp, worktreeReadCommandTimeoutMs); err != nil {
		if isMachineWaking(err) {
			return nil, err
		}
		if req.Msg.Background && machineUnreachable(err) {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("your machine is not connected"))
		}
		logging.Warn("Failed to discover worktrees via daemon", "error", err)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("failed to list git worktrees"))
	}

	// Importing moves the checkout when the workspace layout cannot hold it
	// in place: more than one repo, or a repo below the project root.
	moves := importMoves(byID)

	out := &reliantv1.DiscoverWorktreesResponse{}
	if resp.WorktreesRoot != "" {
		out.WorkspacesRoot = filepath.Join(resp.WorktreesRoot, worktreepath.ProjectDirName(project.Name))
	}
	for _, e := range resp.Worktrees {
		if e.Prunable {
			out.Stale = append(out.Stale, staleToProto(e, byID))
			continue
		}
		d := &reliantv1.DiscoveredWorktree{
			Path: e.Path, Name: e.Name, Branch: e.Branch, RepoId: e.RepoID,
			Head: e.Head, Locked: e.Locked, MovesOnImport: moves,
		}
		if r := byID[e.RepoID]; r != nil {
			d.RepoName = r.Name
		}
		out.Discovered = append(out.Discovered, d)
	}
	return connect.NewResponse(out), nil
}

// PruneWorktrees removes git's records of the project's worktrees whose
// directories no longer exist. Only an explicit request does this.
func (s *WorktreeService) PruneWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.PruneWorktreesRequest],
) (*connect.Response[reliantv1.PruneWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}
	refs, byID, err := s.projectRepoRefs(ctx, project)
	if err != nil {
		return nil, err
	}
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, "prune")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Pruned []discoveredEntry `json:"pruned"`
		Errors []string          `json:"errors"`
	}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.prune", map[string]any{"repos": refs}, &resp, worktreeDaemonCommandTimeoutMs); err != nil {
		if isMachineWaking(err) {
			return nil, err
		}
		logging.Warn("Failed to prune worktrees via daemon", "error", err)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("failed to prune git worktrees"))
	}
	for _, msg := range resp.Errors {
		logging.Warn("worktree prune error", "projectID", project.ID, "error", msg)
	}
	if len(resp.Pruned) == 0 && len(resp.Errors) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("git error: %s", resp.Errors[0]))
	}
	out := &reliantv1.PruneWorktreesResponse{}
	for _, e := range resp.Pruned {
		out.Pruned = append(out.Pruned, staleToProto(e, byID))
	}
	return connect.NewResponse(out), nil
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

	// Recreating unarchives the row, so refuse before touching the machine when
	// its name has been taken since.
	if holder, err := s.database.GetLiveWorktreeByName(ctx, worktree.ProjectID, worktree.Name); err == nil && holder != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"cannot recreate '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
	}

	// Get project for git operations
	project, err := s.database.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.Error("Failed to get project", "error", err, "projectID", worktree.ProjectID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Recreate EVERY repo of the workspace. A multi-repo project's root is not a
	// git repository, so rebuilding it means one `git worktree add` per nested
	// repo, each in its own repository, under <workspace>/<relative path>.
	repos, err := s.database.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}
	type recreateRepo struct {
		RepoPath string `json:"repo_path"`
		Rel      string `json:"rel"`
		Branch   string `json:"branch,omitempty"`
	}
	var recreateRepos []recreateRepo
	for _, repo := range repos {
		recreateRepos = append(recreateRepos, recreateRepo{
			RepoPath: filepath.Join(project.Path, repo.RelativePath),
			Rel:      filepath.ToSlash(repo.RelativePath),
			Branch:   worktree.Branch,
		})
	}
	var recreateResp struct {
		Success      bool   `json:"success"`
		BranchExists bool   `json:"branch_exists"`
		PathExists   bool   `json:"path_exists"`
		Output       string `json:"output,omitempty"`
		Error        string `json:"error,omitempty"`
		// SnapshotApplied: saved work was put back on every checkout that had
		// some. SnapshotFailed names the ones it was not.
		SnapshotApplied bool     `json:"snapshot_applied,omitempty"`
		SnapshotFailed  []string `json:"snapshot_failed,omitempty"`
		// SnapshotBranchMoved: saved work not applied because the branch moved.
		SnapshotBranchMoved []string `json:"snapshot_branch_moved,omitempty"`
	}
	var savedRefs []string
	if worktree.CleanupMetadata != nil {
		savedRefs = worktree.CleanupMetadata.SnapshotRefs
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.recreate", map[string]any{
		"project_path":  project.Path,
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
		"worktree_id":   worktree.ID,
		"snapshot_refs": savedRefs,
		"fence":         strconv.FormatInt(worktree.DeletedAt.UnixNano(), 10),
		"repos":         recreateRepos,
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

	// The directory is back, so it is no longer "removed" or "held". The saved
	// refs are kept until a snapshot has actually been re-applied: they are the
	// only copy of that work, and the archived panel lists them.
	// A failed apply keeps the refs on the row, so the work is never forgotten.
	keep := &db.CleanupMetadata{}
	if len(savedRefs) > 0 && !recreateResp.SnapshotApplied {
		keep.SnapshotRefs = savedRefs
	}
	if len(keep.SnapshotRefs) == 0 {
		keep = nil
	}
	if err := s.database.UpdateWorktreeCleanupMetadata(ctx, req.Msg.WorktreeId, keep); err != nil {
		logging.Warn("Failed to update cleanup metadata", "error", err)
	}

	// Unarchive the worktree
	if err := s.database.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		logging.Error("Failed to unarchive worktree", "error", err, "worktreeID", req.Msg.WorktreeId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unarchive worktree"))
	}

	// Unarchive all chats associated with this worktree
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	out := &reliantv1.RecreateWorktreeResponse{
		Message: "Worktree recreated successfully from branch",
		Path:    worktree.Path,
		Branch:  worktree.Branch,
	}
	switch {
	case len(recreateResp.SnapshotBranchMoved) > 0:
		out.Message = "Worktree recreated, but its saved work was not applied"
		out.SnapshotWarning = fmt.Sprintf("Your work is saved at %s but was not applied because the branch moved since it was saved.", strings.Join(savedRefs, ", "))
		out.SnapshotRefs = savedRefs
	case len(recreateResp.SnapshotFailed) > 0:
		out.Message = "Worktree recreated, but its saved work was not applied"
		out.SnapshotWarning = fmt.Sprintf("Your work is saved at %s but could not be applied (%s).", strings.Join(savedRefs, ", "), strings.Join(recreateResp.SnapshotFailed, ", "))
		out.SnapshotRefs = savedRefs
	}
	return connect.NewResponse(out), nil
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, worktreeOwner(worktree), "worktree.git_changes", map[string]string{
		"worktree_path": repoPath,
		"branch":        worktree.Branch,
		"base_branch":   worktree.BaseBranch,
	}, &changesResp, worktreeReadCommandTimeoutMs); err != nil {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, worktreeOwner(worktree), "worktree.git_status", map[string]string{
		"worktree_path": repoPath,
		"branch":        worktree.Branch,
	}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get git status: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetWorktreeGitStatusResponse{
		WorktreeId:     worktree.ID,
		Path:           repoPath,
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, worktreeOwner(worktree), "worktree.git_commits", commitsReq{
		WorktreePath: repoPath,
		Branch:       worktree.Branch,
		BaseBranch:   baseBranch,
		Limit:        limit,
	}, &commitsResp, worktreeReadCommandTimeoutMs); err != nil {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.stage", stageReq{
		WorktreePath: repoPath,
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.unstage", unstageReq{
		WorktreePath: repoPath,
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Commit via daemon
	output, err := s.commitViaDaemon(ctx, userID, worktreeOwner(worktree), repoPath, req.Msg.Message)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "nothing to commit") || strings.Contains(errStr, "nothing added to commit") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no staged changes to commit; stage files first"))
		}
		logging.Error("Failed to commit changes", "error", err, "path", repoPath)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to commit: %w", err))
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Push via daemon. Branch is resolved daemon-side from HEAD, not from
	// worktree.Branch — the user may have checked out a different branch in
	// this repo since creation.
	output, err := s.pushViaDaemon(ctx, userID, worktreeOwner(worktree), repoPath)
	if err != nil {
		logging.Error("Failed to push changes", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to push: %w", err))
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Pull via daemon. Branch resolved daemon-side from HEAD.
	output, err := s.pullViaDaemon(ctx, userID, worktreeOwner(worktree), repoPath)
	if err != nil {
		logging.Error("Failed to pull changes", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to pull: %w", err))
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	// Branch is resolved daemon-side from HEAD, not from worktree.Branch.
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.get_pr", map[string]string{
		"worktree_path": repoPath,
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

	repoPath, repo, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Route entire create-PR flow through daemon (stage, commit, push, create PR).
	// The daemon resolves the source branch from HEAD on repoPath — worktree.Branch
	// is the creation-time branch and may diverge per-repo after the user checks
	// out something else. We *do* still pass base_branch: it's persisted at
	// create time per-repo (worktree.BaseBranches[repo_id]) so we can honor
	// non-default bases like master/develop/release.
	body := ""
	if req.Msg.Body != nil && *req.Msg.Body != "" {
		body = *req.Msg.Body
	}

	baseBranch := s.resolveRepoBaseBranch(worktree, repo)

	var prResp struct {
		Success       bool   `json:"success"`
		PRURL         string `json:"pr_url,omitempty"`
		Output        string `json:"output,omitempty"`
		AutoCommitted bool   `json:"auto_committed"`
		AutoPushed    bool   `json:"auto_pushed"`
		Error         string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.create_pr", map[string]string{
		"worktree_path": repoPath,
		"title":         req.Msg.Title,
		"body":          body,
		"base_branch":   baseBranch,
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, worktreeOwner(worktree), "worktree.revert", map[string]interface{}{
		"worktree_path": repoPath,
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

// ListWorktreeRepoStatuses returns per-repo git status across every nested
// repo in the worktree's project. Drives the right-sidebar grouped view.
//
// This fans worktree.git_status N times (one per repo) under the workspace
// root. Per-repo failures don't abort the response; the row carries an
// error string so the UI can render "couldn't read this one." Single-repo
// and zero-repo projects collapse to a single-element (or empty) response.
func (s *WorktreeService) ListWorktreeRepoStatuses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorktreeRepoStatusesRequest],
) (*connect.Response[reliantv1.ListWorktreeRepoStatusesResponse], error) {
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

	repos, err := s.database.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list repos for project"))
	}

	// 0-repo project: empty response. The UI treats this as "nothing to show
	// in the sidebar" rather than an error so newly-created projects without
	// any nested repo registered yet still render cleanly.
	if len(repos) == 0 {
		return connect.NewResponse(&reliantv1.ListWorktreeRepoStatusesResponse{
			Statuses: []*reliantv1.WorktreeRepoStatus{},
		}), nil
	}

	statuses := make([]*reliantv1.WorktreeRepoStatus, 0, len(repos))
	for _, r := range repos {
		repoPath := filepath.Join(worktree.Path, r.RelativePath)
		row := &reliantv1.WorktreeRepoStatus{
			RepoId:           r.ID,
			RepoName:         r.Name,
			RepoRelativePath: r.RelativePath,
		}

		var statusResp struct {
			Branch         string   `json:"branch"`
			HasChanges     bool     `json:"has_changes"`
			Status         string   `json:"status"`
			StagedFiles    []string `json:"staged_files"`
			UnstagedFiles  []string `json:"unstaged_files"`
			UntrackedFiles []string `json:"untracked_files"`
			Ahead          int32    `json:"ahead"`
			Behind         int32    `json:"behind"`
			Error          string   `json:"error,omitempty"`
		}
		if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, worktreeOwner(worktree), "worktree.git_status", map[string]string{
			"worktree_path": repoPath,
			"branch":        worktree.Branch,
		}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
			row.Error = err.Error()
			statuses = append(statuses, row)
			continue
		}

		row.CurrentBranch = statusResp.Branch
		row.HasChanges = statusResp.HasChanges
		row.Ahead = statusResp.Ahead
		row.Behind = statusResp.Behind
		row.ChangedFiles = int32(len(statusResp.StagedFiles) + len(statusResp.UnstagedFiles) + len(statusResp.UntrackedFiles))
		if statusResp.Error != "" {
			row.Error = statusResp.Error
		}
		statuses = append(statuses, row)
	}

	return connect.NewResponse(&reliantv1.ListWorktreeRepoStatusesResponse{
		Statuses: statuses,
	}), nil
}
