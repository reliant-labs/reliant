package worktree

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

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
func (s *Service) CreateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorktreeRequest],
) (*connect.Response[reliantv1.CreateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Name == "" {
		return nil, svcerr.InvalidArgument("name is required")
	}
	if req.Msg.Branch == "" {
		return nil, svcerr.InvalidArgument("branch is required")
	}
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	// copy_files are exact paths relative to the workspace root. Rejected
	// HERE, synchronously, so a typo or an escaping path is an error the
	// caller sees — not a workspace that silently lacks its .env.
	copyPaths, err := copypath.CleanAll(req.Msg.CopyFiles)
	if err != nil {
		// The validator's sentence names the entry and the rule it broke.
		return nil, svcerr.InvalidArgument(err.Error())
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
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
			if svcerr.IsNotFound(err) {
				return nil, svcerr.NotFound("source worktree")
			}
			return nil, err
		}
		sourceWorktree, err = s.deps.Repo.GetWorktree(ctx, *req.Msg.SourceWorktreeId)
		if err != nil {
			logging.WarnContext(ctx, "Source worktree not found, falling back to project paths",
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
	ownerDaemonID := placement.DaemonID
	// The checkouts are made after this returns, so an asleep machine would
	// leave a row that fails in the background. Wake it now instead: nothing
	// has been recorded yet, so the client's retry creates the worktree, once,
	// on a machine that is up.
	if err := s.wakeIfAsleep(ctx, userID, placement, project.Path); err != nil {
		return nil, err
	}

	// After placement: an empty registry is reconciled against the machine
	// the checkouts are about to be made on.
	repos, err := s.listProjectRepos(ctx, userID, project, func() (machinewake.Target, error) { return placement, nil })
	if err != nil {
		return nil, err
	}

	// An idempotency key makes a retry safe. Creation returns before the work
	// finishes, so a client that loses the response cannot tell "failed" from
	// "succeeded, reply dropped" — without this the natural retry produces a
	// second workspace with a second on-disk checkout.
	if req.Msg.IdempotencyKey != nil && *req.Msg.IdempotencyKey != "" {
		existing, err := s.deps.Repo.GetWorktreeByIdempotencyKey(ctx, project.ID, *req.Msg.IdempotencyKey)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create worktree"))
		}
		if existing != nil {
			activity.Skip(ctx) // a retry of a create already recorded: no second "create" event
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
	worktree, err := workspacecreate.Insert(ctx, s.deps.Repo, createReq)
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
		return svcerr.AlreadyExists(fmt.Sprintf("worktree with name '%s' already exists in this project; archive or rename it, or choose another name", name))
	case errors.As(err, &branchHeld):
		// Its sentence names the branch and the worktree holding it.
		return svcerr.WithCause(svcerr.FailedPrecondition(branchHeld.Error()), err)
	case svcerr.IsUserError(err):
		// e.g. a branch name git would refuse: its code and its sentence.
		return svcerr.Wrap(err)
	}
	return svcerr.WrapOr(err, svcerr.Internal("failed to create worktree"))
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
func (s *Service) finishWorktreeCreate(ctx context.Context, userID string, req workspacecreate.Request, worktree *db.Worktree) {
	_ = workspacecreate.Finish(ctx, s.deps.Repo, worktreeMachine{s: s, userID: userID, daemonID: req.OwnerDaemonID}, req, worktree)
	s.emitWorktreeChanged(ctx, userID, req.Project.ID, worktree.ID)
}
