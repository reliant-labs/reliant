// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

	// Build a human-readable workspace directory name (e.g.
	// `myproject/feature-branch-a3f1b2c8`) instead of a bare UUID. Disk path
	// becomes <HOME>/.reliant/worktrees/<workspaceID>[/<sub_path>].
	workspaceID := worktreepath.WorkspaceDirName(project.Name, req.Msg.Name)

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
	worktreeID := uuid.New().String()
	now := time.Now().UTC()
	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}
	var idempotencyKey *string
	if req.Msg.IdempotencyKey != nil && *req.Msg.IdempotencyKey != "" {
		idempotencyKey = req.Msg.IdempotencyKey
	}
	worktree := &db.Worktree{
		ID:             worktreeID,
		Name:           req.Msg.Name,
		Branch:         req.Msg.Branch,
		BaseBranch:     globalBase,
		ProjectID:      project.ID,
		ChatID:         chatID,
		DaemonID:       &ownerDaemonID,
		IdempotencyKey: idempotencyKey,
		Status:         int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActive:     now,
	}
	if err := s.database.CreateWorktree(ctx, worktree); err != nil {
		logging.Error("Failed to create worktree row", "error", err)
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "duplicate key") {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project; enable force to override", req.Msg.Name))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create worktree"))
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
		s.finishWorktreeCreate(bgCtx, userID, project, repos, &pending, req.Msg, ownerDaemonID, workspaceID, globalBase, sourceWorkspace, copyPaths)
	}()

	return connect.NewResponse(&reliantv1.CreateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

// worktreeCreateBudget bounds the detached creation goroutine. Generous
// because it covers every repo in a multi-repo project sequentially, but
// finite: without it a wedged daemon would leak a goroutine and leave the row
// in CREATING forever.
const worktreeCreateBudget = 10 * time.Minute

// finishWorktreeCreate performs the daemon-side worktree creation and settles
// the row to ACTIVE or FAILED. It runs on a context detached from the client
// request; nothing here may assume a caller is still listening.
func (s *WorktreeService) finishWorktreeCreate(
	ctx context.Context,
	userID string,
	project *db.Project,
	repos []*core.Repo,
	worktree *db.Worktree,
	msg *reliantv1.CreateWorktreeRequest,
	ownerDaemonID string,
	workspaceID string,
	globalBase string,
	sourceWorkspace string,
	copyPaths []string,
) {
	req := struct {
		Msg *reliantv1.CreateWorktreeRequest
	}{Msg: msg}

	type repoCreateResult struct {
		repo         *core.Repo
		worktreePath string
		baseBranch   string
	}
	successes := make([]repoCreateResult, 0, len(repos))

	// Declared before fail() so the rollback can remove the workspace root.
	// Assigned by the first successful per-repo create below.
	var workspaceRoot string

	// The row is kept as FAILED rather than deleted: a workspace that silently
	// never appears is indistinguishable from a bug, whereas a visible failed
	// row can be retried or dismissed.
	//
	// Creation is ALL-OR-NOTHING on disk. A multi-repo workspace that got
	// three repos in before the fourth failed is not a usable workspace, so
	// the checkouts that DID succeed are torn down rather than left as a
	// partial tree that later reads as real.
	fail := func(reason error) {
		for _, s2 := range successes {
			repoPath := filepath.Join(project.Path, s2.repo.RelativePath)
			_ = s.sendWorktreeDaemonCommand(ctx, userID, ownerDaemonID, "worktree.delete_directory", map[string]string{
				"project_path":  repoPath,
				"worktree_path": s2.worktreePath,
			}, nil)
		}
		// Remove the workspace root itself. The per-repo deletes above clear
		// <root>/<repo.rel>, which leaves the root behind for a multi-repo
		// workspace — an empty directory that looks like a workspace to
		// anything that stats it. Empty-only, for the same reason as the
		// daemon-side rollback.
		if workspaceRoot != "" {
			_ = s.sendWorktreeDaemonCommand(ctx, userID, ownerDaemonID, "worktree.delete_directory", map[string]string{
				"project_path":  project.Path,
				"worktree_path": workspaceRoot,
			}, nil)
		}

		logging.Error("Worktree creation failed", "error", reason, "worktreeID", worktree.ID)
		worktree.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
		// A FAILED row must carry NO path.
		//
		// The row is inserted with an empty path and only settled at the end
		// (see the comment above the ACTIVE branch), so on the common failure
		// path this is already "". It is cleared explicitly because a failure
		// AFTER the path was assigned — the UpdateWorktree error below is
		// exactly that case — would otherwise leave a FAILED row pointing at
		// directories this function just deleted.
		//
		// That combination is what breaks callers: filepreview.ResolveBasePath
		// falls back to the project path only when the worktree LOOKUP errors,
		// so a row that exists with an unusable path is returned as-is and
		// every scoped filesystem RPC fails. Empty is the honest value, and it
		// is the one ResolveBasePath can now recognize.
		worktree.Path = ""
		worktree.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateWorktree(ctx, worktree); err != nil {
			logging.Error("Failed to mark worktree failed", "error", err, "worktreeID", worktree.ID)
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
	}

	// createRepo checks out ONE repo of the workspace. It runs concurrently
	// with its siblings, so it touches nothing shared: everything it learns
	// goes back through its return values, and the caller merges them.
	//
	// The returned error is the user-facing reason; it is non-nil exactly when
	// the repo produced no checkout.
	createRepo := func(repo *core.Repo) (repoCreateResult, error) {
		repoPath := filepath.Join(project.Path, repo.RelativePath)

		// Per-repo override > global > daemon auto-detect (empty).
		repoBase := globalBase
		if v, ok := req.Msg.BaseBranches[repo.ID]; ok && v != "" {
			repoBase = v
		}

		if req.Msg.Force {
			// Stale-branch cleanup; the workspace dir itself is fresh per UUID.
			_ = s.sendWorktreeDaemonCommand(ctx, userID, ownerDaemonID, "worktree.force_cleanup", map[string]string{
				"project_path":  repoPath,
				"worktree_path": "",
				"branch":        req.Msg.Branch,
			}, nil)
		}

		type createReq struct {
			ProjectPath string `json:"project_path"`
			WorkspaceID string `json:"workspace_id"`
			SubPath     string `json:"sub_path"`
			Name        string `json:"name"`
			Branch      string `json:"branch"`
			BaseBranch  string `json:"base_branch"`
			Force       bool   `json:"force"`
			WorktreeID  string `json:"worktree_id"`
		}
		var createResp struct {
			Success      bool   `json:"success"`
			WorktreePath string `json:"worktree_path"`
			BaseBranch   string `json:"base_branch,omitempty"`
			Error        string `json:"error,omitempty"`
		}

		err := s.sendWorktreeDaemonCommand(ctx, userID, ownerDaemonID, "worktree.create", createReq{
			WorktreeID:  worktree.ID,
			ProjectPath: repoPath,
			WorkspaceID: workspaceID,
			SubPath:     repo.RelativePath,
			Name:        req.Msg.Name,
			Branch:      req.Msg.Branch,
			BaseBranch:  repoBase,
			Force:       req.Msg.Force,
		}, &createResp)
		if err != nil {
			logging.Error("Failed to create git worktree via daemon", "error", err, "repo", repo.ID)
			return repoCreateResult{}, fmt.Errorf("failed to create worktree for repo %s: %w", repo.Name, err)
		}
		if !createResp.Success {
			logging.Error("Failed to create git worktree", "error", createResp.Error, "repo", repo.ID)
			return repoCreateResult{}, s.parseGitWorktreeError(createResp.Error, createResp.WorktreePath, req.Msg.Branch, repoBase)
		}
		return repoCreateResult{
			repo:         repo,
			worktreePath: createResp.WorktreePath,
			baseBranch:   firstNonEmpty(createResp.BaseBranch, repoBase),
		}, nil
	}

	// Repos are checked out CONCURRENTLY, not one after another.
	//
	// Each repo is an independent `git worktree add` into its own
	// subdirectory, so serializing them made the wait the SUM of every
	// checkout: a three-repo workspace sat in CREATING for ~29s, ~8s of it in
	// the last repo alone. Run together, the wait is the slowest repo —
	// measured on control-plane/forge/reliant, median 6.6s -> 2.8s.
	//
	// Do not ALSO turn on git's parallel checkout (checkout.workers) for
	// these. It was measured on top of this and made creation slower (2.8s ->
	// 3.9s; 3.6s even capped at 4 workers): the repos already overlap their
	// I/O, and N repos each forking a worker per CPU oversubscribes the
	// machine.
	//
	// Two rules keep that safe:
	//
	//   - A repo nested inside another registered repo waits for its parent
	//     (repopkg.CheckoutWaves). Its checkout lands inside the parent's, and git
	//     refuses to populate a parent into a directory that already exists.
	//     Unrelated repos — the normal layout — form a single wave.
	//
	//   - A failure does NOT cancel its siblings. Cancelling would only stop
	//     the server waiting; the daemon's git keeps running, finishes the
	//     checkout, and the server never learns its path — the orphaned
	//     directory the all-or-nothing rollback exists to prevent. Every
	//     sibling is allowed to finish so each success is known and torn down.
	results := make([]repoCreateResult, len(repos))
	errs := make([]error, len(repos))
	ran := make([]bool, len(repos))
	var firstErr error
	for _, wave := range repopkg.CheckoutWaves(repos) {
		var wg sync.WaitGroup
		for _, i := range wave {
			ran[i] = true
			wg.Go(func() {
				results[i], errs[i] = createRepo(repos[i])
			})
		}
		wg.Wait()

		for _, i := range wave {
			if errs[i] != nil {
				firstErr = errs[i]
				break
			}
		}
		if firstErr != nil {
			break
		}
	}
	// Collected in the order the repos were listed, not the order they
	// finished, so the workspace root and display base come from the same
	// repo on every run — exactly as when the loop was sequential.
	for i := range repos {
		if ran[i] && errs[i] == nil {
			successes = append(successes, results[i])
		}
	}

	// The workspace root comes from the first success: the daemon returned
	// <HOME>/.reliant/worktrees/<workspace_id>[/<repo.rel>], so strip the
	// trailing repo.RelativePath to get the root. Resolved BEFORE fail() so
	// the rollback can remove the root of a partially created workspace.
	if len(successes) > 0 {
		first := successes[0]
		if first.repo.RelativePath == "" {
			workspaceRoot = first.worktreePath
		} else {
			workspaceRoot = strings.TrimSuffix(first.worktreePath,
				string(filepath.Separator)+first.repo.RelativePath)
			if workspaceRoot == first.worktreePath {
				workspaceRoot = filepath.Dir(first.worktreePath)
			}
		}
	}
	if firstErr != nil {
		fail(firstErr)
		return
	}

	// Carry over the requested paths — the gitignored pieces a checkout does
	// not bring — once for the whole workspace, root to root. The workspace
	// mirrors the project's layout, so `reliant/.env` lands in the reliant
	// checkout and a root-level `.env` at the workspace root. Paths were
	// validated in CreateWorktree, before the row existed.
	//
	// A failed copy does not fail the workspace: the checkouts are complete
	// and usable, and a missing .env is something the user can see and fix.
	// Tearing down a good workspace over it would be worse.
	if len(copyPaths) > 0 {
		copySource := project.Path
		if sourceWorkspace != "" {
			copySource = sourceWorkspace
		}
		var copyResp struct {
			Copied  []string          `json:"copied"`
			Missing []string          `json:"missing"`
			Failed  map[string]string `json:"failed"`
			Error   string            `json:"error"`
		}
		err := s.sendWorktreeDaemonCommand(ctx, userID, ownerDaemonID, "worktree.copy_paths", map[string]any{
			"source_root": copySource,
			"dest_root":   workspaceRoot,
			"paths":       copyPaths,
		}, &copyResp)
		switch {
		case err != nil:
			logging.Error("Failed to copy paths into worktree", "error", err, "worktreeID", worktree.ID)
		case copyResp.Error != "" || len(copyResp.Failed) > 0:
			logging.Error("Some paths were not copied into worktree", "error", copyResp.Error,
				"failed", copyResp.Failed, "worktreeID", worktree.ID)
		case len(copyResp.Missing) > 0:
			logging.Info("Requested copy paths not present in source", "missing", copyResp.Missing,
				"source", copySource, "worktreeID", worktree.ID)
		}
	}

	// Persist one Worktree row representing the workspace. BaseBranch is
	// recorded as the resolved value of the first repo for display.
	// BaseBranches captures the per-repo resolved value for every successful
	// create — this is what CreatePR consults at op time so e.g. a repo whose
	// default is `master` doesn't get a PR opened against `main`.
	displayBase := ""
	baseBranches := make(map[string]string, len(successes))
	if len(successes) > 0 {
		displayBase = successes[0].baseBranch
	}
	for _, s := range successes {
		if s.baseBranch != "" {
			baseBranches[s.repo.ID] = s.baseBranch
		}
	}
	// Single-repo legacy: BaseBranch alone is canonical, BaseBranches stays
	// nil so the column is NULL and writers don't have to special-case the
	// "one entry" map.
	if len(baseBranches) <= 1 {
		baseBranches = nil
	}

	// Settle the row the handler already inserted. The daemon is the only
	// source of the resolved path and base branches, so those are unknown
	// until now — which is why the CREATING row carries an empty path.
	worktree.Path = workspaceRoot
	worktree.BaseBranch = displayBase
	worktree.BaseBranches = baseBranches
	worktree.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	worktree.UpdatedAt = time.Now().UTC()
	worktree.LastActive = worktree.UpdatedAt

	if err := s.database.UpdateWorktree(ctx, worktree); err != nil {
		// The checkouts exist on disk but the row cannot record them, so the
		// directories would be unreachable. Tear them down and mark failed.
		fail(fmt.Errorf("failed to record worktree: %w", err))
		return
	}
	s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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

	// Resolve the daemon that will validate (and therefore owns on disk) the
	// imported worktree — the chat's machine when one is named — so the same
	// id is recorded on the row and tool execution routes back to the machine
	// the checkout actually lives on.
	placement, err := s.placeNewWorktree(ctx, userID, req.Msg.ProjectId, req.Msg.ChatId, nil, "import")
	if err != nil {
		return nil, err
	}
	ownerDaemonID := placement.daemonID

	// Validate path exists and is a git worktree via daemon
	var importResp struct {
		Valid      bool   `json:"valid"`
		AbsPath    string `json:"abs_path"`
		Branch     string `json:"branch"`
		BaseBranch string `json:"base_branch"`
		Error      string `json:"error,omitempty"`
	}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.import_validate", map[string]string{"path": req.Msg.Path}, &importResp, worktreeDaemonCommandTimeoutMs); err != nil {
		if isMachineWaking(err) {
			return nil, err
		}
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
	// Archived rows count: an archived workspace's directory is still the
	// machine's to settle, and a second row sharing its path would let the
	// archive be removed out from under the new one.
	existingWorktrees, err := s.database.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID:       &req.Msg.ProjectId,
		IncludeArchived: true,
		Limit:           1000,
	})
	if err != nil {
		logging.Error("Failed to list worktrees", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check for existing worktrees"))
	}

	for _, wt := range existingWorktrees {
		if wt.Name == name && wt.DeletedAt == nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree with name '%s' already exists in this project", name))
		}
		if wt.Path == absPath {
			if wt.DeletedAt != nil {
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("worktree at path '%s' belongs to an archived workspace; restore it instead", absPath))
			}
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
		DaemonID:   &ownerDaemonID,
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
	// Project-level: the project clone, not any one worktree, so default
	// resolution as before.
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "", "worktree.discover", map[string]string{"project_path": project.Path}, &discoverResp); err != nil {
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
