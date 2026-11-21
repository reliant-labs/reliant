package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

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
func (s *Service) ImportWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ImportWorktreeRequest],
) (*connect.Response[reliantv1.ImportWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Path == "" {
		return nil, svcerr.InvalidArgument("path is required")
	}
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	// An empty registry is reconciled against the machine the import would
	// be placed on; a populated one reaches no daemon before confirm_move.
	place := func() (placed, error) {
		placement, err := s.placeNewWorktree(ctx, userID, project.ID, req.Msg.ChatId, nil, req.Msg.DaemonId, "import")
		if err != nil {
			return placed{}, err
		}
		return s.withCheckout(ctx, userID, project, placement, "import a worktree into it")
	}
	repos, err := s.listProjectRepos(ctx, userID, project, place)
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
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, "repo not in project")
		}
	case len(repos) == 1:
		repo = repos[0]
	default:
		return nil, svcerr.InvalidArgument("repo_id is required in multi-repo projects")
	}
	moves := importMoves(byID)
	if moves && !req.Msg.ConfirmMove {
		return nil, svcerr.InvalidArgument("importing this worktree moves its directory into a new workspace; confirm the move (confirm_move) to continue")
	}

	target, err := place()
	if err != nil {
		return nil, err
	}
	placement := target.machine
	if err := s.wakeIfAsleep(ctx, userID, placement, target.checkout.Path); err != nil {
		return nil, err
	}
	ownerDaemonID := placement.DaemonID

	// Re-discover on the daemon and take the entry from there.
	exclude, err := s.trackedWorktreePaths(ctx, project.ID, placement.DaemonID)
	if err != nil {
		return nil, err
	}
	repoPath := target.checkout.Join(repo.RelativePath)
	var found struct {
		Worktrees []discoveredEntry `json:"worktrees"`
	}
	payload := map[string]any{"repos": []discoverRepoRef{{RepoID: repo.ID, Path: repoPath}}, "exclude_roots": exclude}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.discover_repos", payload, &found, worktreeReadCommandTimeoutMs); err != nil {
		return nil, importWorktreeError("validate path", err)
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
		return nil, svcerr.InvalidArgument(fmt.Sprintf("%s is not an untracked linked worktree of repo '%s'; refresh the list and try again", req.Msg.Path, repo.Name))
	}
	if entry.Branch == "" {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("%s has a detached HEAD, so there is no branch to adopt; create one in it first (git switch -c <branch>) and try again", entry.Path))
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
		Checkout:      target.checkout.Path,
		Repos:         repos,
		Name:          name,
		Branch:        entry.Branch,
		ChatID:        chatID,
		OwnerDaemonID: ownerDaemonID,
	}
	if moves {
		createReq.WorkspaceID = worktreepath.WorkspaceDirName(project.Name, name)
	}
	machine := worktreeMachine{s: s, userID: userID, target: placement}
	worktree, err := workspacecreate.Insert(ctx, s.deps.Repo, machine, createReq)
	if err != nil {
		return nil, insertWorktreeError(err, name)
	}

	if !moves {
		var def struct {
			DefaultBranch string `json:"default_branch"`
		}
		_ = machine.Send(ctx, "worktree.get_default_branch", map[string]string{"repo_path": repoPath}, &def, worktreeReadCommandTimeoutMs)
		createReq.Existing = map[string]workspacecreate.ExistingCheckout{
			repo.ID: {Path: entry.Path, Base: def.DefaultBranch},
		}
		if err := workspacecreate.Finish(ctx, s.deps.Repo, machine, createReq, worktree); err != nil {
			s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
			return nil, importWorktreeError("import worktree", err)
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
		if err := s.deps.Repo.UpdateWorktree(ctx, worktree); err != nil {
			logging.ErrorContext(ctx, "Failed to mark adopted worktree failed", "error", err, "worktreeID", worktree.ID)
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
		if moveErr != nil {
			return nil, importWorktreeError("move the worktree", moveErr)
		}
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("could not move %s: %s", entry.Path, moved.Error))
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
		s.finishWorktreeCreate(bgCtx, userID, placement, createReq, &pending)
	}()
	return connect.NewResponse(&reliantv1.ImportWorktreeResponse{Worktree: worktreeToProto(worktree)}), nil
}

// workspaceMoveTimeoutMs bounds adopt_move: a rename on one filesystem, but
// git also rewrites the worktree's records.
const workspaceMoveTimeoutMs int32 = 60_000
